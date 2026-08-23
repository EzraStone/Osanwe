// Command invitebook creates one-shot voucher books and a runtime manifest
// that cannot group vouchers by invite. Run it offline, distribute one secret
// book to each invitee, and copy only invite-manifest.json to the mint.
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/EzraStone/osanwe/internal/mint"
)

func main() {
	if err := run(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("invitebook", flag.ContinueOnError)
	fs.SetOutput(stderr)
	program := fs.String("program", "", "short unique id for this fixed beta cohort")
	mintKeyID := fs.String("mint-key-id", "", "dedicated mint key id for this cohort")
	notBeforeText := fs.String("not-before", "", "issuance start as a whole-second RFC3339 UTC time ending in Z")
	notAfterText := fs.String("not-after", "", "exclusive issuance end as a whole-second RFC3339 UTC time ending in Z")
	seats := fs.Int("seats", 0, "number of invite books to generate (required)")
	vouchers := fs.Int("vouchers-per-invite", 0, "one-shot vouchers in each invite book (required)")
	out := fs.String("out", "", "new output directory; must not already exist")
	fs.Usage = func() {
		fmt.Fprintln(stderr, "invitebook — generate fixed-window free-beta voucher books offline.")
		fmt.Fprintln(stderr)
		fmt.Fprintln(stderr, "The command never prints seeds or vouchers. Copy only invite-manifest.json to the mint.")
		fmt.Fprintln(stderr, "Retained seed books can regroup issuance by invite; generate outside synced or version-controlled storage.")
		fmt.Fprintln(stderr)
		fmt.Fprintln(stderr, "Flags:")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("invitebook: unexpected positional arguments: %v", fs.Args())
	}
	if *program == "" || *mintKeyID == "" || *notBeforeText == "" || *notAfterText == "" ||
		*seats == 0 || *vouchers == 0 || *out == "" {
		return errors.New("invitebook: -program, -mint-key-id, -not-before, -not-after, -seats, -vouchers-per-invite, and -out are all required")
	}
	notBefore, err := time.Parse("2006-01-02T15:04:05Z", *notBeforeText)
	if err != nil || notBefore.Format("2006-01-02T15:04:05Z") != *notBeforeText {
		return errors.New("invitebook: -not-before must be a whole-second RFC3339 UTC time ending in Z")
	}
	notAfter, err := time.Parse("2006-01-02T15:04:05Z", *notAfterText)
	if err != nil || notAfter.Format("2006-01-02T15:04:05Z") != *notAfterText {
		return errors.New("invitebook: -not-after must be a whole-second RFC3339 UTC time ending in Z")
	}
	if err := mint.GenerateInviteBooks(mint.InviteBookGenerationConfig{
		ProgramID:         *program,
		MintKeyID:         *mintKeyID,
		NotBefore:         notBefore,
		NotAfter:          notAfter,
		Seats:             *seats,
		VouchersPerInvite: *vouchers,
		OutputDir:         *out,
	}); err != nil {
		return fmt.Errorf("invitebook: %w", err)
	}

	// Deliberately report only public capacity and paths. Secret seeds remain
	// in owner-only files under books/ and never enter terminal history.
	fmt.Fprintf(stdout, "Generated %d invite book(s), %d voucher(s) each.\n", *seats, *vouchers)
	fmt.Fprintf(stdout, "Mint manifest: %s\n", *out+string(os.PathSeparator)+"invite-manifest.json")
	fmt.Fprintf(stdout, "Secret books: %s\n", *out+string(os.PathSeparator)+"books")
	fmt.Fprintln(stdout, "Keep books off the mint host and out of synced/version-controlled storage; retained mapped copies weaken issuer-side unlinkability.")
	return nil
}
