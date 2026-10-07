package document

import (
	"bytes"
	"errors"
	"fmt"
	"net/netip"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"

	"drool/internal/firewall/policy"
)

// The YAML form, e.g.
//
//	networks:
//	  admin-lan:
//	    cidr: 192.168.1.0/24
//	groups:
//	  ssh-managed:
//	    members: [server-a, server-b]
//	services:
//	  app-8812:
//	    protocol: tcp
//	    port: 8812
//	policy:
//	  - name: admin-to-jump
//	    from: admin-lan
//	    to: jump-a
//	    allow: [ssh]
//
// References may be bare names when they are unambiguous; the canonical
// rendering prefixes a name (host:, group:, network:) only when needed.
// Comments, anchors and formatting are not preserved.

type yDoc struct {
	Networks map[string]yNetwork `yaml:"networks,omitempty"`
	Groups   map[string]yGroup   `yaml:"groups,omitempty"`
	Services map[string]yService `yaml:"services,omitempty"`
	Policy   []yRule             `yaml:"policy"`
}

type yNetwork struct {
	Description string   `yaml:"description,omitempty"`
	CIDR        string   `yaml:"cidr,omitempty"`
	CIDRs       []string `yaml:"cidrs,omitempty"`
}

type yGroup struct {
	Description string   `yaml:"description,omitempty"`
	Members     []string `yaml:"members"`
}

type yService struct {
	Description string   `yaml:"description,omitempty"`
	Protocol    string   `yaml:"protocol,omitempty"`
	Port        portVal  `yaml:"port,omitempty"`
	Ports       portVal  `yaml:"ports,omitempty"`
	Entries     []yEntry `yaml:"entries,omitempty"`
}

type yEntry struct {
	Protocol string  `yaml:"protocol"`
	Port     portVal `yaml:"port,omitempty"`
	Ports    portVal `yaml:"ports,omitempty"`
	Type     string  `yaml:"type,omitempty"`
}

type yRule struct {
	Name        string    `yaml:"name"`
	Description string    `yaml:"description,omitempty"`
	Enabled     *bool     `yaml:"enabled,omitempty"`
	From        strOrList `yaml:"from"`
	To          strOrList `yaml:"to"`
	Allow       svcList   `yaml:"allow,omitempty"`
	Deny        svcList   `yaml:"deny,omitempty"`
}

// svcList reads one service or a list and is always written as a list.
type svcList []string

func (s *svcList) UnmarshalYAML(n *yaml.Node) error { return (*strOrList)(s).UnmarshalYAML(n) }

// portVal is "8812", "8000-8010" or "80,443"; a single port is written as
// a number.
type portVal string

func (p portVal) MarshalYAML() (any, error) {
	if n, err := strconv.Atoi(string(p)); err == nil {
		return n, nil
	}
	return string(p), nil
}

func (p *portVal) UnmarshalYAML(n *yaml.Node) error {
	switch n.Kind {
	case yaml.ScalarNode:
		*p = portVal(n.Value)
	case yaml.SequenceNode:
		var parts []string
		for _, c := range n.Content {
			if c.Kind != yaml.ScalarNode {
				return fmt.Errorf("line %d: ports must be numbers or ranges", c.Line)
			}
			parts = append(parts, c.Value)
		}
		*p = portVal(strings.Join(parts, ","))
	default:
		return fmt.Errorf("line %d: a port is a number, a range (8000-8010) or a list", n.Line)
	}
	return nil
}

// strOrList is a single reference or a list.
type strOrList []string

func (s strOrList) MarshalYAML() (any, error) {
	if len(s) == 1 {
		return s[0], nil
	}
	return []string(s), nil
}

func (s *strOrList) UnmarshalYAML(n *yaml.Node) error {
	switch n.Kind {
	case yaml.ScalarNode:
		*s = strOrList{n.Value}
	case yaml.SequenceNode:
		var out []string
		for _, c := range n.Content {
			if c.Kind != yaml.ScalarNode {
				return fmt.Errorf("line %d: expected a name, not a %s", c.Line, kindName(c.Kind))
			}
			out = append(out, c.Value)
		}
		*s = out
	default:
		return fmt.Errorf("line %d: expected a name or a list of names", n.Line)
	}
	return nil
}

func kindName(k yaml.Kind) string {
	switch k {
	case yaml.MappingNode:
		return "mapping"
	case yaml.SequenceNode:
		return "list"
	}
	return "value"
}

// Names is what bare references are resolved against.
type Names struct {
	Hosts  []string
	Groups []string // stored groups (the document's own are added)
}

type nameIndex map[string][]string // lower(name) → kinds

func (d Document) index(n Names) nameIndex {
	idx := nameIndex{}
	addKind := func(name, kind string) {
		k := lower(name)
		if !slices.Contains(idx[k], kind) {
			idx[k] = append(idx[k], kind)
		}
	}
	for _, h := range n.Hosts {
		addKind(h, RefHost)
	}
	for _, g := range n.Groups {
		addKind(g, RefGroup)
	}
	for _, g := range d.Groups {
		addKind(g.Name, RefGroup)
	}
	for _, nw := range d.Networks {
		addKind(nw.Name, RefNetwork)
	}
	return idx
}

// yamlRef renders a reference, bare when unambiguous.
func (idx nameIndex) yamlRef(s string) string {
	ref, err := ParseRef(s)
	if err != nil {
		return s
	}
	switch ref.Kind {
	case RefInternet, RefInternal, RefAny:
		return ref.Kind
	case RefCIDR:
		return ref.Name
	}
	if len(idx[lower(ref.Name)]) == 1 && !slices.Contains(Keywords, lower(ref.Name)) && !looksLikeCIDR(ref.Name) {
		return ref.Name
	}
	return ref.String()
}

func looksLikeCIDR(s string) bool {
	_, err := netip.ParsePrefix(s)
	return err == nil
}

// docRef resolves a YAML reference to canonical form.
func (idx nameIndex) docRef(s string) (string, error) {
	s = strings.TrimSpace(s)
	if slices.Contains(Keywords, s) {
		return s, nil
	}
	if p, err := netip.ParsePrefix(s); err == nil {
		return "cidr:" + p.Masked().String(), nil
	}
	if a, err := netip.ParseAddr(s); err == nil {
		bits := 32
		if a.Is6() {
			bits = 128
		}
		return "cidr:" + netip.PrefixFrom(a, bits).String(), nil
	}
	if strings.Contains(s, ":") {
		ref, err := ParseRef(s)
		if err != nil {
			return "", err
		}
		return ref.String(), nil
	}
	kinds := idx[lower(s)]
	switch len(kinds) {
	case 0:
		return "", fmt.Errorf("unknown name %q (not a host, group or network; write internet, internal, any or a CIDR for addresses)", s)
	case 1:
		return kinds[0] + ":" + s, nil
	}
	return "", fmt.Errorf("%q is ambiguous (it is a %s): write %s:%s or %s:%s", s, strings.Join(kinds, " and a "), kinds[0], s, kinds[1], s)
}

// RenderYAML is the canonical YAML of a document.
func RenderYAML(d Document, n Names) []byte {
	d = Normalize(d)
	idx := d.index(n)
	y := yDoc{Policy: []yRule{}}
	if len(d.Networks) > 0 {
		y.Networks = map[string]yNetwork{}
		for _, nw := range d.Networks {
			yn := yNetwork{Description: nw.Description}
			if len(nw.CIDRs) == 1 {
				yn.CIDR = nw.CIDRs[0]
			} else {
				yn.CIDRs = nw.CIDRs
			}
			y.Networks[nw.Name] = yn
		}
	}
	if len(d.Groups) > 0 {
		y.Groups = map[string]yGroup{}
		for _, g := range d.Groups {
			y.Groups[g.Name] = yGroup{Description: g.Description, Members: g.Members}
		}
	}
	if len(d.Services) > 0 {
		y.Services = map[string]yService{}
		for _, s := range d.Services {
			ys := yService{Description: s.Description}
			if len(s.Entries) == 1 && (s.Entries[0].Protocol == policy.TCP || s.Entries[0].Protocol == policy.UDP) {
				ys.Protocol, ys.Port = s.Entries[0].Protocol, portVal(s.Entries[0].Ports)
			} else {
				for _, e := range s.Entries {
					ys.Entries = append(ys.Entries, yEntry{Protocol: e.Protocol, Port: portVal(e.Ports), Type: e.ICMPType})
				}
			}
			y.Services[s.Name] = ys
		}
	}
	for _, r := range d.Policy {
		yr := yRule{Name: r.Name, Description: r.Description}
		if !r.Enabled {
			f := false
			yr.Enabled = &f
		}
		for _, f := range r.From {
			yr.From = append(yr.From, idx.yamlRef(f))
		}
		for _, t := range r.To {
			yr.To = append(yr.To, idx.yamlRef(t))
		}
		if r.Action == policy.Deny {
			yr.Deny = r.Services
		} else {
			yr.Allow = r.Services
		}
		y.Policy = append(y.Policy, yr)
	}
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	enc.Encode(y)
	enc.Close()
	return buf.Bytes()
}

var lineRE = regexp.MustCompile(`^(?:yaml: )?line (\d+): (.*)$`)

// ParseYAML reads the YAML form. Syntax and structure problems carry line
// numbers; the result still has to pass Resolve.
func ParseYAML(src []byte, n Names) (Document, []Problem) {
	var probs []Problem
	if len(bytes.TrimSpace(src)) == 0 {
		return Normalize(Document{}), nil
	}
	var root yaml.Node
	if err := yaml.Unmarshal(src, &root); err != nil {
		return Document{}, yamlProblems(err)
	}
	var y yDoc
	dec := yaml.NewDecoder(bytes.NewReader(src))
	dec.KnownFields(true)
	if err := dec.Decode(&y); err != nil {
		return Document{}, yamlProblems(err)
	}
	lines := lineIndex(&root)
	d := Document{}
	for name, yn := range y.Networks {
		nw := Network{Name: name, Description: yn.Description, CIDRs: yn.CIDRs}
		if yn.CIDR != "" {
			nw.CIDRs = append([]string{yn.CIDR}, nw.CIDRs...)
		}
		d.Networks = append(d.Networks, nw)
	}
	for name, yg := range y.Groups {
		d.Groups = append(d.Groups, Group{Name: name, Description: yg.Description, Members: yg.Members})
	}
	for name, ys := range y.Services {
		s := Service{Name: name, Description: ys.Description}
		path := "services." + name
		switch {
		case len(ys.Entries) > 0 && (ys.Protocol != "" || ys.Port != "" || ys.Ports != ""):
			probs = append(probs, Problem{Path: path, Line: lines[path], Message: "use either protocol/port or entries, not both"})
		case len(ys.Entries) > 0:
			for _, e := range ys.Entries {
				s.Entries = append(s.Entries, policy.Entry{Protocol: e.Protocol, Ports: string(firstNonEmpty(e.Port, e.Ports)), ICMPType: e.Type})
			}
		case ys.Protocol == "":
			probs = append(probs, Problem{Path: path, Line: lines[path], Message: "a service needs a protocol (tcp, udp, icmp or icmpv6) and, for tcp/udp, a port"})
		default:
			s.Entries = []policy.Entry{{Protocol: ys.Protocol, Ports: string(firstNonEmpty(ys.Port, ys.Ports))}}
		}
		d.Services = append(d.Services, s)
	}
	idx := d.index(n)
	for i, yr := range y.Policy {
		path := fmt.Sprintf("policy[%d]", i)
		r := Rule{Name: yr.Name, Description: yr.Description, Enabled: yr.Enabled == nil || *yr.Enabled}
		switch {
		case len(yr.Allow) > 0 && len(yr.Deny) > 0:
			probs = append(probs, Problem{Path: path, Line: lines[path], Message: "a policy either allows or denies: use allow or deny, not both"})
		case len(yr.Deny) > 0:
			r.Action, r.Services = policy.Deny, yr.Deny
		case len(yr.Allow) > 0:
			r.Action, r.Services = policy.Allow, yr.Allow
		default:
			probs = append(probs, Problem{Path: path, Line: lines[path], Message: "a policy needs allow: [services] or deny: [services]"})
		}
		for side, refs := range map[string]strOrList{"from": yr.From, "to": yr.To} {
			// (from/to map order doesn't matter: each side appends to its own list)
			for j, s := range refs {
				ref, err := idx.docRef(s)
				if err != nil {
					probs = append(probs, Problem{Path: fmt.Sprintf("%s.%s[%d]", path, side, j), Line: lines[path+"."+side], Message: err.Error()})
					continue
				}
				if side == "from" {
					r.From = append(r.From, ref)
				} else {
					r.To = append(r.To, ref)
				}
			}
		}
		d.Policy = append(d.Policy, r)
	}
	return Normalize(d), probs
}

func firstNonEmpty(a, b portVal) portVal {
	if a != "" {
		return a
	}
	return b
}

func yamlProblems(err error) []Problem {
	var te *yaml.TypeError
	msgs := []string{err.Error()}
	if errors.As(err, &te) {
		msgs = te.Errors
	}
	var out []Problem
	for _, m := range msgs {
		p := Problem{Message: m}
		if sm := lineRE.FindStringSubmatch(m); sm != nil {
			p.Line, _ = strconv.Atoi(sm[1])
			p.Message = friendlyYAML(sm[2])
		}
		out = append(out, p)
	}
	return out
}

var (
	unknownFieldRE = regexp.MustCompile(`field (\S+) not found in type document\.y(\w+)`)
	cannotRE       = regexp.MustCompile(`cannot unmarshal !!(\w+) .*into .*`)
)

func friendlyYAML(m string) string {
	if sm := unknownFieldRE.FindStringSubmatch(m); sm != nil {
		where := map[string]string{"Doc": "at the top level (networks, groups, services, policy)", "Network": "in a network (description, cidr, cidrs)",
			"Group": "in a group (description, members)", "Service": "in a service (description, protocol, port, entries)",
			"Entry": "in a service entry (protocol, port, type)", "Rule": "in a policy (name, description, enabled, from, to, allow, deny)"}[sm[2]]
		return fmt.Sprintf("unknown field %q %s", sm[1], where)
	}
	if sm := cannotRE.FindStringSubmatch(m); sm != nil {
		return fmt.Sprintf("unexpected %s here (%s)", map[string]string{"seq": "list", "map": "mapping", "str": "text", "int": "number"}[sm[1]], m)
	}
	return m
}

// lineIndex maps paths (services.NAME, policy[i], policy[i].from, ...) to
// their YAML lines.
func lineIndex(root *yaml.Node) map[string]int {
	out := map[string]int{}
	if root.Kind != yaml.DocumentNode || len(root.Content) == 0 || root.Content[0].Kind != yaml.MappingNode {
		return out
	}
	top := root.Content[0]
	for i := 0; i+1 < len(top.Content); i += 2 {
		key, val := top.Content[i].Value, top.Content[i+1]
		switch {
		case key == "policy" && val.Kind == yaml.SequenceNode:
			for j, item := range val.Content {
				p := fmt.Sprintf("policy[%d]", j)
				out[p] = item.Line
				if item.Kind == yaml.MappingNode {
					for k := 0; k+1 < len(item.Content); k += 2 {
						out[p+"."+item.Content[k].Value] = item.Content[k].Line
					}
				}
			}
		case val.Kind == yaml.MappingNode:
			for k := 0; k+1 < len(val.Content); k += 2 {
				out[key+"."+val.Content[k].Value] = val.Content[k].Line
			}
		}
	}
	return out
}

// Locate adds YAML line numbers to resolution problems (paths such as
// "policy[3] web.from[0]" or "groups.lan.members").
func Locate(src []byte, probs []Problem) []Problem {
	var root yaml.Node
	if yaml.Unmarshal(src, &root) != nil {
		return probs
	}
	lines := lineIndex(&root)
	for i, p := range probs {
		if p.Line != 0 {
			continue
		}
		path := p.Path
		// "policy[3] web.from[0]" → "policy[3]" + ".from"
		if strings.HasPrefix(path, "policy[") {
			idx := path[:strings.Index(path, "]")+1]
			rest := ""
			if dot := strings.LastIndex(path, "."); dot > len(idx) {
				rest = path[dot:]
				if b := strings.Index(rest, "["); b > 0 {
					rest = rest[:b]
				}
			}
			if l := lines[idx+rest]; l > 0 {
				probs[i].Line = l
			} else {
				probs[i].Line = lines[idx]
			}
			continue
		}
		for len(path) > 0 {
			if l := lines[path]; l > 0 {
				probs[i].Line = l
				break
			}
			dot := strings.LastIndex(path, ".")
			if dot < 0 {
				break
			}
			path = path[:dot]
		}
	}
	return probs
}
