package bearer

import (
	"context"
	"errors"
	"net"
	"strings"
)

// Failures reach a person, so they are written for one.
//
// The raw text of a transport error describes the machinery: "dial tcp
// 127.0.0.1:8443: connect: connection refused" is precise, useless to whoever
// is holding the keyboard, and says nothing about what to do next. It is still
// worth keeping -- an operator debugging a relay wants exactly that string --
// so both are returned: a sentence for the person, and the original alongside
// it for whoever is reading logs.
//
// Nothing here invents a diagnosis. Where the cause is genuinely unclear the
// message says so rather than guessing, because a confident wrong explanation
// sends someone off to fix the wrong thing.

// explain turns a transport error into a sentence, and reports whether it
// found anything better than the original.
func explain(err error) (string, bool) {
	if err == nil {
		return "", false
	}

	switch {
	case errors.Is(err, context.Canceled):
		return "The request was cancelled before it finished.", true

	case errors.Is(err, context.DeadlineExceeded) || isTimeout(err):
		return "The provider took too long to answer. Nothing was lost; try again.", true
	}

	text := err.Error()
	switch {
	case strings.Contains(text, "connection refused"):
		return "The relay is not answering. If it was chosen from a directory another will be tried; " +
			"if it was pinned by hand, its operator needs to bring it back.", true

	case strings.Contains(text, "relay key mismatch"):
		return "The relay presented a different key from the one it published. " +
			"That is either a rotation its operator did not announce, or something pretending to be it. " +
			"Nothing was sent.", true

	case strings.Contains(text, "rejected the credential"), strings.Contains(text, "(407)"):
		return "The relay refused this client's credential. OSANWE_SECRET does not match what it was started with.", true

	case strings.Contains(text, "will not carry traffic"), strings.Contains(text, "(403)"):
		return "The relay will not carry traffic to this destination. Its operator has to allow it.", true

	case strings.Contains(text, "no relay could carry"), strings.Contains(text, "no relays known"):
		return "No relay is available to carry this request. The directory may be unreachable, " +
			"or every relay it lists may be down.", true

	case strings.Contains(text, "every relay rejected the credential"):
		return "Every relay refused this client's credential, so the secret is the problem rather than the network.", true

	case strings.Contains(text, "x509"), strings.Contains(text, "certificate"):
		return "The certificate presented on the way to the provider could not be verified. " +
			"Nothing was sent.", true

	case strings.Contains(text, "no such host"):
		return "The provider's address could not be resolved.", true

	case strings.Contains(text, "EOF"):
		return "The connection ended before an answer arrived.", true
	}

	// Unrecognised. Say that plainly rather than dressing up the original as
	// an explanation it is not.
	return "The request did not get through.", false
}

func isTimeout(err error) bool {
	var ne net.Error
	return errors.As(err, &ne) && ne.Timeout()
}
