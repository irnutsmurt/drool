package agentsvc

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"drool/internal/store/db"
)

// Shepherd/controller compatibility (M16, ARCHITECTURE-ALPHA §33.8). The
// protocol range is negotiated at every hello (incompatible: no session);
// within it, optional capabilities are announced as features and every
// operation that needs one is gated on it before anything is sent, so a
// Shepherd is never asked to do something it doesn't understand.

// Feature is an optional Shepherd capability.
type Feature struct {
	Name string // as announced in the hello
	What string // what it enables, in plain words
}

// KnownFeatures are the features this controller uses.
var KnownFeatures = []Feature{
	{"firewall-1", "firewall management"},
	{"netmon-1", "network activity"},
	{"monitor-1", "system monitoring"},
	{"query-1", "Desired State package and file checks"},
	{"epoch-1", "moving to a restored controller's epoch"},
}

// Compatibility statuses, worst first.
const (
	CompatTrustFailure    = "trust_failure"
	CompatIncompatible    = "incompatible"
	CompatUpdateRequired  = "update_required"
	CompatReduced         = "reduced"
	CompatUpdateAvailable = "update_recommended"
	CompatOK              = "compatible"
)

// Compatibility is how a Shepherd and this controller work together.
type Compatibility struct {
	Status  string   `json:"status"`
	Summary string   `json:"summary"`
	Missing []string `json:"missing"` // capabilities this Shepherd lacks, in plain words
}

// IdentityFeatures returns the features an identity last announced.
func IdentityFeatures(id db.AgentIdentity) []string {
	var f []string
	json.Unmarshal([]byte(id.FeaturesJson), &f)
	return f
}

// Judge decides a Shepherd's compatibility. needed maps features this
// host's configuration depends on to why (e.g. "firewall-1" → "its
// firewall is managed"); update is a newer release ("" when none);
// recoveryGrace is how long an expired certificate can still be renewed.
func Judge(id db.AgentIdentity, features []string, needed map[string]string, update string, recoveryGrace time.Duration, now time.Time) Compatibility {
	has := map[string]bool{}
	for _, f := range features {
		has[f] = true
	}
	switch {
	case id.Status == "revoked":
		return Compatibility{Status: CompatTrustFailure, Summary: "its identity is revoked: enroll the Shepherd again", Missing: []string{}}
	case id.Status == "quarantined":
		return Compatibility{Status: CompatTrustFailure, Summary: "identity conflict: two machines present this identity", Missing: []string{}}
	case id.Compatible == 0:
		return Compatibility{Status: CompatIncompatible, Summary: "its protocol isn't compatible with this controller: " + id.CompatReason, Missing: []string{}}
	case id.CertNotAfter > 0 && now.Sub(time.UnixMilli(id.CertNotAfter)) > recoveryGrace:
		return Compatibility{Status: CompatTrustFailure, Missing: []string{},
			Summary: fmt.Sprintf("its certificate expired %s ago, beyond the %s it can be renewed in: enroll the Shepherd again",
				now.Sub(time.UnixMilli(id.CertNotAfter)).Round(time.Hour), recoveryGrace)}
	}
	var missing, required []string
	for _, f := range KnownFeatures {
		if has[f.Name] {
			continue
		}
		missing = append(missing, f.What)
		if why, ok := needed[f.Name]; ok {
			required = append(required, f.What+" ("+why+")")
		}
	}
	sort.Strings(required)
	c := Compatibility{Missing: missing}
	if c.Missing == nil {
		c.Missing = []string{}
	}
	version := id.AgentVersion
	if version == "" {
		version = "unknown"
	}
	switch {
	case len(required) > 0:
		c.Status, c.Summary = CompatUpdateRequired, "update it: this version ("+version+") can't do "+strings.Join(required, "; ")
	case len(missing) > 0 && len(features) > 0:
		c.Status, c.Summary = CompatReduced, "works, without "+strings.Join(missing, ", ")+" (version "+version+")"
		if update != "" {
			c.Summary += "; " + update + " is available"
		}
	case len(missing) > 0:
		// Announced nothing: a Shepherd from before features existed.
		c.Status, c.Summary = CompatReduced, "works, without "+strings.Join(missing, ", ")+" (version "+version+", from before these were announced)"
	case update != "":
		c.Status, c.Summary = CompatUpdateAvailable, "works; version "+update+" is available"
	default:
		c.Status, c.Summary = CompatOK, "connected and compatible"
	}
	return c
}
