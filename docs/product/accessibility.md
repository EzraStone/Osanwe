# Accessibility acceptance criteria

Privacy controls are not meaningful if a person cannot perceive or operate them.

## Keyboard

- Every action is reachable in a logical order without a pointer.
- Tab lists use one tab stop and support Left, Right, Home, and End.
- Dialogs move focus inside, contain it while open, close with Escape, and restore the opener.
- Hidden views and closed dialogs contain no focusable controls.
- Sending with Enter remains optional through Shift+Enter for a newline.

## Screen readers

- Navigation, tabs, tab panels, model lists, forms, status, and dialogs have programmatic names.
- Streaming output is announced without rereading the entire transcript.
- Busy, stopped, failed, saved, exported, and deleted states are conveyed without color alone.
- Privacy facts are text, not icon-only grades.

## Visual presentation

- Text and controls meet WCAG 2.2 AA contrast.
- Focus indicators remain visible in both palettes.
- The layout works at 320 CSS pixels and at 200 percent zoom without horizontal page scrolling.
- User font sizing is not disabled.
- Reduced-motion preferences suppress decorative animation.

## Cognitive clarity

- Labels use plain language before protocol names.
- Destructive actions name what they delete and what they cannot delete.
- Retention mode is visible near the conversation, not buried in settings.
- Unknown provider policy is displayed as unknown rather than omitted.
- No countdown, urgency, or credit-expiry language pressures a payment.

## Verification

Automated markup and interaction tests are necessary but insufficient. Before a private beta, test
Chat, Models, Connect, local retention, export, and deletion using keyboard-only navigation and at
least one current screen reader on each supported desktop platform.
