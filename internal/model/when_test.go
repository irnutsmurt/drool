package model

import (
	"encoding/json"
	"testing"
)

func TestScriptBodyJSON(t *testing.T) {
	var op Operation
	if err := json.Unmarshal([]byte(`{"type":"script","body":{"debian":"a","default":"b"},"when":{"os_family":["Debian"]}}`), &op); err != nil {
		t.Fatal(err)
	}
	if err := op.Normalize(); err != nil {
		t.Fatal(err)
	}
	if op.Script.When.OSFamily[0] != "debian" || op.Script.Body.ByOS["default"] != "b" {
		t.Fatalf("decoded %+v", op.Script)
	}
	b, _ := json.Marshal(op)
	var back Operation
	if err := json.Unmarshal(b, &back); err != nil || back.Script.Body.ByOS["debian"] != "a" {
		t.Fatalf("round trip %s: %v", b, err)
	}
	if err := json.Unmarshal([]byte(`{"type":"script","body":{"debian":"a"},"when":{"os_family":["windows"]}}`), &op); err != nil {
		t.Fatal(err)
	}
	if err := op.Normalize(); err == nil {
		t.Fatal("unknown OS family accepted")
	}
}

func TestForHost(t *testing.T) {
	op := Operation{Type: OpScript, Script: &ScriptOp{Body: ScriptBody{ByOS: map[string]string{
		"ubuntu": "u", "debian": "d", "default": "x"}}}}
	cases := []struct {
		os   HostOS
		want string
	}{
		{HostOS{Family: "debian", Distro: "ubuntu", Known: true}, "u"}, // distro beats family
		{HostOS{Family: "debian", Distro: "debian", Known: true}, "d"},
		{HostOS{Family: "redhat", Distro: "rocky", Known: true}, "x"}, // default
		{HostOS{}, "x"}, // unknown facts use default
	}
	for _, c := range cases {
		got, ok, why := op.ForHost(c.os)
		if !ok || got.Script.Body.Text != c.want {
			t.Errorf("%+v: got %q ok=%v why=%q", c.os, got.Script.Body.Text, ok, why)
		}
	}
	noDefault := Operation{Type: OpScript, Script: &ScriptOp{Body: ScriptBody{ByOS: map[string]string{"debian": "d"}}}}
	if _, ok, _ := noDefault.ForHost(HostOS{}); ok {
		t.Fatal("unknown facts without default should skip")
	}
}
