package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"

	"drool/internal/agentrpc"
	"drool/internal/agentsvc"
	"drool/internal/store"
	"drool/internal/version"
)

// buildInfo is what `drool version --json` prints and the API's system
// information reports: enough to identify the exact build for support.
type buildInfo struct {
	version.Build
	// SchemaVersion is the newest database schema this binary migrates to.
	SchemaVersion int `json:"schema_version"`
	// AgentProtocol is the agent protocol range this build speaks; the
	// product version doesn't decide compatibility (ARCHITECTURE-ALPHA §34.2).
	AgentProtocol struct {
		Min uint32 `json:"min"`
		Max uint32 `json:"max"`
	} `json:"agent_protocol"`
	// AgentFeatures are the optional Shepherd capabilities this controller uses.
	AgentFeatures []string `json:"agent_features"`
}

func currentBuild() buildInfo {
	b := buildInfo{Build: version.Get(), SchemaVersion: store.LatestVersion()}
	r := agentrpc.Local()
	b.AgentProtocol.Min, b.AgentProtocol.Max = r.Min, r.Max
	for _, f := range agentsvc.KnownFeatures {
		b.AgentFeatures = append(b.AgentFeatures, f.Name)
	}
	return b
}

func cmdVersion(args []string) error {
	fs := flag.NewFlagSet("version", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "print JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	b := currentBuild()
	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(b)
	}
	fmt.Printf("drool %s (commit %s, built %s)\n", b.Version, b.ShortCommit(), b.BuildDate)
	fmt.Printf("  channel          %s\n", b.Channel)
	fmt.Printf("  source           %s\n", b.Source())
	fmt.Printf("  commit           %s\n", b.Commit)
	fmt.Printf("  database schema  %d\n", b.SchemaVersion)
	fmt.Printf("  agent protocol   %d–%d (Shepherd features: %s)\n", b.AgentProtocol.Min, b.AgentProtocol.Max, strings.Join(b.AgentFeatures, ", "))
	fmt.Printf("  go               %s %s\n", b.GoVersion, b.Platform)
	return nil
}
