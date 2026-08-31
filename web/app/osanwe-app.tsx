'use client';

import { useEffect, useMemo, useRef, useState } from 'react';
import {
  ArrowUp,
  BriefcaseBusiness,
  Code2,
  KeyRound,
  MessageCircle,
  Moon,
  Plus,
  Settings,
  ShieldCheck,
  Sun,
} from 'lucide-react';

import { Button } from '@/components/ui/button';
import { Checkbox } from '@/components/ui/checkbox';
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog';
import { Input } from '@/components/ui/input';
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select';
import { Textarea } from '@/components/ui/textarea';

type ProviderId = 'groq' | 'openai';
type Mode = 'chat' | 'code';
type ChatMessage = {
  id: string;
  role: 'user' | 'assistant';
  content: string;
};

const PROVIDERS: Record<
  ProviderId,
  { label: string; keyHint: string; models: { id: string; label: string }[] }
> = {
  groq: {
    label: 'Groq',
    keyHint: 'gsk_…',
    models: [
      { id: 'openai/gpt-oss-20b', label: 'GPT-OSS 20B' },
      { id: 'openai/gpt-oss-120b', label: 'GPT-OSS 120B' },
    ],
  },
  openai: {
    label: 'OpenAI',
    keyHint: 'sk-…',
    models: [
      { id: 'gpt-5.6-luna', label: 'GPT-5.6 Luna' },
      { id: 'gpt-5.6-terra', label: 'GPT-5.6 Terra' },
      { id: 'gpt-5.6-sol', label: 'GPT-5.6 Sol' },
    ],
  },
};

function messageId() {
  return globalThis.crypto?.randomUUID?.() ?? `${Date.now()}-${Math.random()}`;
}

export function OsanweApp() {
  const [mode, setMode] = useState<Mode>('chat');
  const [theme, setTheme] = useState<'light' | 'dark'>('dark');
  const [settingsOpen, setSettingsOpen] = useState(false);
  const [provider, setProvider] = useState<ProviderId>('groq');
  const [model, setModel] = useState(PROVIDERS.groq.models[0].id);
  const [keyDraft, setKeyDraft] = useState('');
  const [providerKey, setProviderKey] = useState('');
  const [consent, setConsent] = useState(false);
  const [messages, setMessages] = useState<ChatMessage[]>([]);
  const [draft, setDraft] = useState('');
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');
  const inputRef = useRef<HTMLTextAreaElement>(null);

  const providerConfig = PROVIDERS[provider];
  const selectedModel = useMemo(
    () => providerConfig.models.find((option) => option.id === model),
    [model, providerConfig.models],
  );

  useEffect(() => {
    document.documentElement.classList.toggle('dark', theme === 'dark');
  }, [theme]);

  useEffect(() => {
    const forgetKey = () => setProviderKey('');
    window.addEventListener('pagehide', forgetKey);
    return () => window.removeEventListener('pagehide', forgetKey);
  }, []);

  function changeProvider(nextProvider: ProviderId) {
    setProvider(nextProvider);
    setModel(PROVIDERS[nextProvider].models[0].id);
    setProviderKey('');
    setKeyDraft('');
    setConsent(false);
    setError('');
  }

  function connectKey() {
    const candidate = keyDraft.trim();
    if (!consent) {
      setError('Confirm the hosted privacy boundary before connecting a key.');
      return;
    }
    const hasUnsafeCharacter = Array.from(candidate).some((character) => {
      const codePoint = character.codePointAt(0) ?? 0;
      return codePoint <= 32 || codePoint === 127;
    });
    if (!candidate || hasUnsafeCharacter) {
      setError('Paste one API key without spaces or line breaks.');
      return;
    }
    setProviderKey(candidate);
    setKeyDraft('');
    setError('');
  }

  function forgetKey() {
    setProviderKey('');
    setKeyDraft('');
    setConsent(false);
    setError('');
  }

  async function sendMessage() {
    const content = draft.trim();
    if (!content || !providerKey || busy) return;

    const userMessage: ChatMessage = { id: messageId(), role: 'user', content };
    const requestMessages = [...messages, userMessage].map(({ role, content: text }) => ({
      role,
      content: text,
    }));

    setMessages((current) => [...current, userMessage]);
    setDraft('');
    setBusy(true);
    setError('');

    try {
      const response = await fetch('/api/chat', {
        method: 'POST',
        headers: {
          'content-type': 'application/json',
          authorization: `Bearer ${providerKey}`,
        },
        body: JSON.stringify({ provider, model, mode, messages: requestMessages }),
      });
      const result = (await response.json()) as { output?: string; error?: string };
      if (!response.ok || !result.output) {
        throw new Error(result.error || `The provider returned HTTP ${response.status}.`);
      }
      setMessages((current) => [
        ...current,
        { id: messageId(), role: 'assistant', content: result.output as string },
      ]);
    } catch (caught) {
      setError(caught instanceof Error ? caught.message : 'The request could not be completed.');
    } finally {
      setBusy(false);
      requestAnimationFrame(() => inputRef.current?.focus());
    }
  }

  function newConversation() {
    setMessages([]);
    setDraft('');
    setError('');
    requestAnimationFrame(() => inputRef.current?.focus());
  }

  return (
    <main className="app-shell">
      <header className="topbar">
        <button type="button" className="wordmark" onClick={newConversation} aria-label="Start a new Osanwë conversation">
          <span className="wordmark-mark" aria-hidden="true">⌁</span>
          <span>Osanwë</span>
        </button>

        <nav className="mode-tabs" aria-label="Modes">
          <button
            type="button"
            className={mode === 'chat' ? 'mode-tab is-active' : 'mode-tab'}
            onClick={() => setMode('chat')}
            aria-current={mode === 'chat' ? 'page' : undefined}
          >
            <MessageCircle aria-hidden="true" /> Chat
          </button>
          <button
            type="button"
            className={mode === 'code' ? 'mode-tab is-active' : 'mode-tab'}
            onClick={() => setMode('code')}
            aria-current={mode === 'code' ? 'page' : undefined}
          >
            <Code2 aria-hidden="true" /> Code
          </button>
          <button type="button" className="mode-tab" disabled>
            <BriefcaseBusiness aria-hidden="true" /> Cowork <small>Soon</small>
          </button>
        </nav>

        <div className="top-actions">
          <Button
            type="button"
            variant="outline"
            size="icon"
            className="top-action"
            onClick={() => setTheme((current) => (current === 'dark' ? 'light' : 'dark'))}
            aria-label={theme === 'dark' ? 'Use light theme' : 'Use dark theme'}
            title={theme === 'dark' ? 'Use light theme' : 'Use dark theme'}
          >
            {theme === 'dark' ? <Sun /> : <Moon />}
          </Button>
          <Button
            type="button"
            variant="outline"
            className="top-action settings-action"
            onClick={() => setSettingsOpen(true)}
          >
            <Settings /> <span>Settings</span>
          </Button>
          <Button
            type="button"
            variant="outline"
            className="top-action"
            onClick={newConversation}
          >
            <Plus /> <span>New</span>
          </Button>
        </div>
      </header>

      <section className="chat-surface" aria-label={`${mode} conversation`}>
        <div className="status-line">
          <span className="status-label">Hosted compatibility beta</span>
          <span className={providerKey ? 'connection is-connected' : 'connection'}>
            <span aria-hidden="true" />
            {providerKey ? `${providerConfig.label} connected` : 'Provider not connected'}
          </span>
        </div>

        <div className="conversation" aria-live="polite" aria-busy={busy}>
          {messages.length === 0 ? (
            <div className="opening">
              <p className="eyebrow">{mode === 'chat' ? 'A private-minded chat' : 'A focused code assistant'}</p>
              <h1>{mode === 'chat' ? 'What are you thinking about?' : 'What should we build?'}</h1>
              <p>
                {providerKey
                  ? `Ready to use ${selectedModel?.label ?? model} through ${providerConfig.label}.`
                  : 'Connect your own provider key in Settings to begin.'}
              </p>
              <div className="boundary-card">
                <ShieldCheck aria-hidden="true" />
                <div>
                  <strong>Know this boundary before you type.</strong>
                  <span>
                    This hosted build sends your key and conversation through Osanwë’s host to your
                    provider. It is not the anonymous relay path. Use synthetic or deliberately
                    non-sensitive prompts.
                  </span>
                </div>
              </div>
              {!providerKey && (
                <Button className="connect-callout" onClick={() => setSettingsOpen(true)}>
                  <KeyRound /> Connect a provider
                </Button>
              )}
            </div>
          ) : (
            <div className="message-list">
              {messages.map((message) => (
                <article className={`message ${message.role}`} key={message.id}>
                  <p className="message-label">{message.role === 'user' ? 'You' : 'Osanwë'}</p>
                  <div>{message.content}</div>
                </article>
              ))}
              {busy && (
                <article className="message assistant pending">
                  <p className="message-label">Osanwë</p>
                  <div><span /><span /><span /></div>
                </article>
              )}
            </div>
          )}
        </div>

        <div className="composer-area">
          {error && <p className="request-error" role="alert">{error}</p>}
          <div className="composer">
            <Textarea
              ref={inputRef}
              value={draft}
              onChange={(event) => setDraft(event.target.value)}
              onKeyDown={(event) => {
                if (event.key === 'Enter' && !event.shiftKey) {
                  event.preventDefault();
                  void sendMessage();
                }
              }}
              placeholder={providerKey ? (mode === 'chat' ? 'Ask anything' : 'Describe a coding task') : 'Connect a provider in Settings'}
              disabled={!providerKey || busy}
              aria-label="Your message"
            />
            <Button
              type="button"
              size="icon"
              className="send-button"
              onClick={() => void sendMessage()}
              disabled={!providerKey || !draft.trim() || busy}
              aria-label="Send message"
            >
              <ArrowUp />
            </Button>
          </div>
          <div className="composer-meta">
            <button type="button" className="model-summary" onClick={() => setSettingsOpen(true)}>
              <span>{selectedModel?.label ?? model}</span>
              <small>{providerConfig.label} · provider billing applies</small>
            </button>
            <span>Key held in this page only</span>
          </div>
        </div>
      </section>

      <Dialog open={settingsOpen} onOpenChange={setSettingsOpen}>
        <DialogContent className="settings-dialog">
          <DialogHeader className="settings-header">
            <p className="eyebrow">Osanwë</p>
            <DialogTitle>Settings</DialogTitle>
            <DialogDescription>
              Connect a provider for this browser tab. Osanwë does not save the key in browser
              storage, cookies, or a database.
            </DialogDescription>
          </DialogHeader>

          <div className="settings-section">
            <div className="settings-section-title">
              <div>
                <h2>Provider access</h2>
                <p>Your provider account pays for every request.</p>
              </div>
              <span className={providerKey ? 'key-state connected' : 'key-state'}>
                {providerKey ? 'Connected' : 'Not connected'}
              </span>
            </div>

            <label className="field-label" htmlFor="provider-select">Provider</label>
            <Select value={provider} onValueChange={(value) => changeProvider(value as ProviderId)}>
              <SelectTrigger id="provider-select" className="settings-select">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="groq">Groq</SelectItem>
                <SelectItem value="openai">OpenAI</SelectItem>
              </SelectContent>
            </Select>

            <label className="field-label" htmlFor="model-select">Model</label>
            <Select value={model} onValueChange={(value) => { if (value) setModel(value); }}>
              <SelectTrigger id="model-select" className="settings-select">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {providerConfig.models.map((option) => (
                  <SelectItem key={option.id} value={option.id}>{option.label}</SelectItem>
                ))}
              </SelectContent>
            </Select>

            {!providerKey ? (
              <>
                <label className="field-label" htmlFor="provider-key">{providerConfig.label} API key</label>
                <Input
                  id="provider-key"
                  type="password"
                  value={keyDraft}
                  onChange={(event) => setKeyDraft(event.target.value)}
                  placeholder={providerConfig.keyHint}
                  autoComplete="new-password"
                  spellCheck={false}
                  onKeyDown={(event) => {
                    if (event.key === 'Enter') {
                      event.preventDefault();
                      connectKey();
                    }
                  }}
                />
                <div className="consent-row">
                  <Checkbox id="hosted-consent" checked={consent} onCheckedChange={(checked) => setConsent(checked === true)} />
                  <label htmlFor="hosted-consent">
                    I understand that my API key, prompts, answers, timing, and IP-related hosting
                    metadata pass through the hosted service and my provider.
                  </label>
                </div>
                <Button className="settings-primary" onClick={connectKey}>
                  Use for this tab
                </Button>
              </>
            ) : (
              <div className="connected-key-row">
                <div><KeyRound /><span>{providerConfig.label} key is in page memory</span></div>
                <Button variant="outline" onClick={forgetKey}>Forget key</Button>
              </div>
            )}
            {error && <p className="settings-error" role="alert">{error}</p>}
          </div>

          <div className="settings-section privacy-note">
            <ShieldCheck />
            <div>
              <h2>For the strongest Osanwë privacy path</h2>
              <p>
                Use the downloadable local client with an independently operated relay. This hosted
                compatibility beta is deliberately easier to try, but it cannot make the same
                separation claim.
              </p>
            </div>
          </div>
        </DialogContent>
      </Dialog>
    </main>
  );
}
