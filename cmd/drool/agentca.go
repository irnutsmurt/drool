package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"drool/internal/agentpki"
	"drool/internal/agentsvc"
	"drool/internal/audit"
	"drool/internal/logging"
	"drool/internal/release"
	"drool/internal/secrets"
	"drool/internal/store"
)

// --- drool release: offline release signing (never touches a controller) ---

func cmdRelease(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: drool release keygen|sign|verify ...")
	}
	switch args[0] {
	case "keygen":
		fs := flag.NewFlagSet("release keygen", flag.ContinueOnError)
		out := fs.String("o", "drool-release", "output name: writes NAME.key (private, keep offline) and NAME.pub")
		name := fs.String("name", "", "label for the public key")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		pub, priv, err := release.GenerateKey()
		if err != nil {
			return err
		}
		if _, err := os.Stat(*out + ".key"); err == nil {
			return fmt.Errorf("%s.key exists; refusing to overwrite a signing key", *out)
		}
		if err := os.WriteFile(*out+".key", priv, 0o600); err != nil {
			return err
		}
		label := *name
		if label == "" {
			label = *out
		}
		if err := os.WriteFile(*out+".pub", []byte(release.FormatPublicKey(pub, label)+"\n"), 0o644); err != nil {
			return err
		}
		fmt.Printf("Wrote %s.key (the release-signing PRIVATE key: keep it offline, never on the controller)\n", *out)
		fmt.Printf("Wrote %s.pub (key id %s): register it under Agents → Updates and install it on hosts\n", *out, release.KeyID(pub))
		return nil
	case "sign":
		fs := flag.NewFlagSet("release sign", flag.ContinueOnError)
		keyFile := fs.String("key", "", "release-signing private key (NAME.key)")
		ver := fs.String("version", "", "agent version (default: the one stamped into the binary, which it must match)")
		channel := fs.String("channel", "stable", "release channel")
		goos := fs.String("os", "", "default: the binary's")
		arch := fs.String("arch", "", "default: the binary's")
		pmin := fs.Uint("protocol-min", 1, "lowest agent protocol the binary speaks")
		pmax := fs.Uint("protocol-max", 1, "highest agent protocol the binary speaks")
		minFrom := fs.String("min-upgrade-from", "", "oldest version that may upgrade straight to this one")
		allowModified := fs.Bool("allow-modified", false, "sign a build made from source with uncommitted changes")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if fs.NArg() != 1 || *keyFile == "" {
			return errors.New("usage: drool release sign --key NAME.key [--version vX.Y.Z] BINARY")
		}
		// What the binary says about itself decides: an agent runs the new
		// binary's "version" before switching and refuses any other answer.
		info, err := release.Inspect(fs.Arg(0))
		if err != nil {
			return err
		}
		m := release.Manifest{Version: *ver, Channel: *channel, OS: *goos, Arch: *arch,
			ProtocolMin: uint32(*pmin), ProtocolMax: uint32(*pmax), MinFrom: *minFrom}
		if err := release.CheckAgentBinary(info, m); err != nil {
			return fmt.Errorf("refusing to sign %s: %w", fs.Arg(0), err)
		}
		if info.Modified && !*allowModified {
			return fmt.Errorf("refusing to sign %s: it was built from source with uncommitted changes (commit %.12s); official releases are clean builds. --allow-modified signs it anyway", fs.Arg(0), info.Revision)
		}
		if m.Version == "" {
			m.Version = info.Version
		}
		if m.OS == "" {
			m.OS = info.OS
		}
		if m.Arch == "" {
			m.Arch = info.Arch
		}
		kb, err := os.ReadFile(*keyFile)
		if err != nil {
			return err
		}
		key, err := release.ParsePrivateKey(kb)
		if err != nil {
			return err
		}
		f, err := os.Open(fs.Arg(0))
		if err != nil {
			return err
		}
		defer f.Close()
		s, err := release.Sign(key, m, f)
		if err != nil {
			return err
		}
		out := fs.Arg(0) + ".release.json"
		b, _ := json.MarshalIndent(s, "", "  ")
		if err := os.WriteFile(out, b, 0o644); err != nil {
			return err
		}
		fmt.Printf("Signed %s %s (%s/%s) with key %s → %s\n", fs.Arg(0), m.Version, m.OS, m.Arch, s.KeyID, out)
		return nil
	case "verify":
		fs := flag.NewFlagSet("release verify", flag.ContinueOnError)
		pubFile := fs.String("key", "", "release public key file (NAME.pub)")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if fs.NArg() != 2 || *pubFile == "" {
			return errors.New("usage: drool release verify --key NAME.pub BINARY.release.json BINARY")
		}
		pb, err := os.ReadFile(*pubFile)
		if err != nil {
			return err
		}
		keys, err := release.ParsePublicKeys(pb)
		if err != nil {
			return err
		}
		mb, err := os.ReadFile(fs.Arg(0))
		if err != nil {
			return err
		}
		s, err := agentsvc.ParseSignedRelease(mb)
		if err != nil {
			return err
		}
		m, err := release.Verify(s, keys)
		if err != nil {
			return err
		}
		f, err := os.Open(fs.Arg(1))
		if err != nil {
			return err
		}
		defer f.Close()
		if err := release.CheckBinary(m, f); err != nil {
			return err
		}
		fmt.Printf("OK: %s %s (%s/%s, protocol %d–%d), sha256 %s\n", m.Product, m.Version, m.OS, m.Arch, m.ProtocolMin, m.ProtocolMax, m.SHA256)
		return nil
	}
	return fmt.Errorf("unknown release command %q", args[0])
}

// --- drool agent-ca: the Agent PKI ---

func cmdAgentCA(cfgPath string, explicit bool, args []string) error {
	if len(args) == 0 {
		return errors.New("usage: drool agent-ca status|export-root|remove-root|rotate|sign-manifest|import-manifest")
	}
	// sign-manifest is fully offline: root key and certificate files only.
	if args[0] == "sign-manifest" {
		return cmdSignManifest(args[1:])
	}
	cfg, err := loadConfig(cfgPath, explicit)
	if err != nil {
		return err
	}
	ctx := context.Background()
	// Changes need the controller stopped: a running controller holds the
	// PKI in memory (use the web UI while it runs).
	changes := args[0] != "status" && args[0] != "export-root"
	if changes {
		unlock, err := store.LockDataDir(cfg.DataDir)
		if err != nil {
			if errors.Is(err, store.ErrLocked) {
				return fmt.Errorf("the controller is running; use Agents → Trust in the web UI, or stop it first")
			}
			return err
		}
		defer unlock()
	}
	st, _, err := openStore(ctx, cfg)
	if err != nil {
		return err
	}
	defer st.Close()
	box, _, _, err := loadMasterKey(ctx, cfg, st)
	if err != nil {
		return err
	}
	pki, _, err := agentsvc.OpenPKI(ctx, st, box, installationName())
	if errors.Is(err, agentsvc.ErrPKIUnusable) && args[0] == "rotate" {
		return recoverIntermediate(ctx, st, box, args[1:])
	}
	if err != nil {
		return err
	}
	logs := logging.Discard()
	aw := audit.NewWriter(st, logs.Audit)
	actor := audit.Actor{Type: audit.ActorSystem, Name: "cli " + currentUser()}
	switch args[0] {
	case "status":
		s := pki.Status()
		fmt.Printf("Agent Root CA:     %s (until %s)\n", s.RootFingerprint, s.RootNotAfter.Format(time.DateOnly))
		if s.RootKeyOnline {
			fmt.Println("Root private key:  ONLINE on this controller — export it to offline storage and remove it")
		} else {
			fmt.Println("Root private key:  offline (removed from this controller)")
		}
		fmt.Printf("Intermediate CA:   %s (until %s)\nTrust manifest:    version %d, %d trusted, %d revoked intermediate(s)\n",
			s.IntermediateFingerprint, s.IntermediateNotAfter.Format(time.DateOnly), s.ManifestVersion, len(s.TrustedIntermediates), len(s.RevokedIntermediates))
		return nil
	case "export-root":
		fs := flag.NewFlagSet("export-root", flag.ContinueOnError)
		out := fs.String("o", "drool-agent-root", "writes NAME.key (private, 0600) and NAME.crt")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		cert, key, err := pki.ExportRoot()
		if err != nil {
			return err
		}
		if err := os.WriteFile(*out+".key", key, 0o600); err != nil {
			return err
		}
		if err := os.WriteFile(*out+".crt", cert, 0o644); err != nil {
			return err
		}
		aw.Record(ctx, audit.Event{Action: "agent_ca.export_root", Actor: &actor, TargetType: "agent_ca"})
		fmt.Printf("Wrote %s.key and %s.crt. Store the key offline (and in a second place), then run `drool agent-ca remove-root --confirm %s`.\n", *out, *out, pki.RootFingerprint())
		return nil
	case "remove-root":
		fs := flag.NewFlagSet("remove-root", flag.ContinueOnError)
		confirm := fs.String("confirm", "", "the root fingerprint")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if *confirm != pki.RootFingerprint() {
			return fmt.Errorf("confirm with --confirm %s (after exporting the key)", pki.RootFingerprint())
		}
		if err := pki.RemoveRootKey(ctx); err != nil {
			return err
		}
		aw.Record(ctx, audit.Event{Action: "agent_ca.remove_root", Actor: &actor, TargetType: "agent_ca"})
		fmt.Println("The root private key was removed from the controller (and from future backups).")
		return nil
	case "rotate":
		fs := flag.NewFlagSet("rotate", flag.ContinueOnError)
		rootKey := fs.String("root-key", "", "the offline root key file (if removed from the controller)")
		revoke := fs.Bool("revoke-old", false, "revoke the current intermediate (after a compromise)")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		var kb []byte
		if *rootKey != "" {
			if kb, err = os.ReadFile(*rootKey); err != nil {
				return err
			}
		}
		v, err := pki.Rotate(ctx, kb, *revoke)
		if err != nil {
			return err
		}
		aw.Record(ctx, audit.Event{Action: "agent_ca.rotate", Actor: &actor, TargetType: "agent_ca", Details: map[string]any{"manifest_version": v, "revoke_old": *revoke}})
		fmt.Printf("New intermediate CA and trust manifest %d. Agents take it the next time they connect.\n", v)
		return nil
	case "import-manifest":
		if len(args) != 2 {
			return errors.New("usage: drool agent-ca import-manifest FILE")
		}
		b, err := os.ReadFile(args[1])
		if err != nil {
			return err
		}
		var sm agentpki.SignedManifest
		if err := json.Unmarshal(b, &sm); err != nil {
			return err
		}
		v, err := pki.ImportManifest(ctx, &sm)
		if err != nil {
			return err
		}
		aw.Record(ctx, audit.Event{Action: "agent_ca.import_manifest", Actor: &actor, TargetType: "agent_ca", Details: map[string]any{"manifest_version": v}})
		fmt.Printf("Imported trust manifest %d.\n", v)
		return nil
	}
	return fmt.Errorf("unknown agent-ca command %q", args[0])
}

// recoverIntermediate handles `rotate` when the intermediate key is gone.
func recoverIntermediate(ctx context.Context, st *store.Store, box *secrets.Box, args []string) error {
	fs := flag.NewFlagSet("rotate", flag.ContinueOnError)
	rootKey := fs.String("root-key", "", "the offline root key file")
	fs.Bool("revoke-old", true, "")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *rootKey == "" {
		return errors.New("the intermediate CA key is gone (lost master key): give the offline root key with --root-key FILE")
	}
	kb, err := os.ReadFile(*rootKey)
	if err != nil {
		return err
	}
	v, err := agentsvc.RecoverIntermediate(ctx, st, box, kb)
	if err != nil {
		return err
	}
	fmt.Printf("Issued a new intermediate CA and trust manifest %d. Start the controller; agents move to the new intermediate on their own (certificate recovery).\n", v)
	return nil
}

// cmdSignManifest signs a trust manifest with an offline root key.
func cmdSignManifest(args []string) error {
	fs := flag.NewFlagSet("sign-manifest", flag.ContinueOnError)
	keyFile := fs.String("root-key", "", "root private key file")
	certFile := fs.String("root-cert", "", "root certificate file")
	ver := fs.Uint64("version", 0, "manifest version (higher than the current one)")
	trust := fs.String("trust", "", "comma-separated trusted intermediate fingerprints")
	revoke := fs.String("revoke", "", "comma-separated revoked intermediate fingerprints")
	out := fs.String("o", "manifest.json", "output file")
	if err := fs.Parse(args); err != nil {
		return err
	}
	kb, err := os.ReadFile(*keyFile)
	if err != nil {
		return err
	}
	cb, err := os.ReadFile(*certFile)
	if err != nil {
		return err
	}
	key, err := agentpki.ParseKeyPEM(kb)
	if err != nil {
		return err
	}
	certs, err := agentpki.ParseCertsPEM(cb)
	if err != nil {
		return err
	}
	if !agentpki.MatchesKey(certs[0], key) {
		return errors.New("the key does not belong to that root certificate")
	}
	split := func(s string) []string {
		var o []string
		for _, f := range strings.Split(s, ",") {
			if f = strings.TrimSpace(f); f != "" {
				o = append(o, f)
			}
		}
		return o
	}
	sm, err := agentpki.SignManifest(&agentpki.CA{Cert: certs[0], Key: key}, agentpki.Manifest{Version: *ver, IssuedAt: time.Now().UTC(),
		TrustedIntermediates: split(*trust), RevokedIntermediates: split(*revoke)})
	if err != nil {
		return err
	}
	b, _ := json.MarshalIndent(sm, "", "  ")
	if err := os.WriteFile(*out, b, 0o644); err != nil {
		return err
	}
	fmt.Printf("Wrote trust manifest %d to %s; import it with `drool agent-ca import-manifest` or Agents → Trust in the web UI.\n", *ver, *out)
	return nil
}

func currentUser() string {
	if u := os.Getenv("SUDO_USER"); u != "" {
		return u
	}
	if u := os.Getenv("USER"); u != "" {
		return u
	}
	return "unknown"
}
