package apps

// Conversion of a docker-compose file into a manifest.
//
// A compose file pasted by an administrator is untrusted input: it may have
// been copied from anywhere. It is not run by a second engine. It is parsed,
// converted into the module's own manifest structure and validated by the
// same strict validator as a shipped manifest; from then on the application
// is installed, updated, backed up and restored like any catalog app.
//
// Only a subset of the compose specification is understood. Everything
// else is refused with an explanation; nothing is silently dropped. All
// problems of a file are collected and reported together.
//
// This file is pure logic: it does not touch Docker, the database or the
// file system.

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Custom applications.
const (
	// CustomPrefix starts the slug of every application added from a
	// compose file. Shipped manifests may not use it, so a custom slug can
	// never collide with a catalog slug.
	CustomPrefix = "custom-"
	// CustomCategory is the category of custom applications ("Özel").
	CustomCategory = "custom"
	// CustomIcon is the generic icon of custom applications.
	CustomIcon = "custom.svg"

	customDescription = "Compose dosyasından eklenen özel uygulama"
)

// Limits on an uploaded compose file.
const (
	// MaxComposeBytes is the largest compose file accepted.
	MaxComposeBytes = 64 << 10
	// maxComposeNodes bounds the YAML tree after anchors and aliases are
	// expanded; a "billion laughs" document fails here.
	maxComposeNodes = 20000
	// maxComposeDepth bounds the nesting of the YAML tree.
	maxComposeDepth    = 24
	maxComposeServices = 12
	maxComposePorts    = 64
	maxComposeVolumes  = 64
	maxComposeEnv      = 256
	maxComposeProblems = 100
)

// IsCustomSlug reports whether slug belongs to a custom application.
func IsCustomSlug(slug string) bool { return strings.HasPrefix(slug, CustomPrefix) }

// ComposeIssue is one problem or note of a conversion.
type ComposeIssue struct {
	Service string `json:"service"`
	Key     string `json:"key"`
	Message string `json:"message"`
}

// Text renders the issue as one Turkish sentence.
func (i ComposeIssue) Text() string {
	var b strings.Builder
	if i.Service != "" {
		b.WriteString(`servis "` + i.Service + `"`)
	}
	if i.Key != "" {
		if b.Len() > 0 {
			b.WriteString(", ")
		}
		b.WriteString(i.Key)
	}
	if b.Len() > 0 {
		b.WriteString(": ")
	}
	b.WriteString(i.Message)
	return b.String()
}

// ComposeError lists every reason a compose file was refused.
type ComposeError struct{ Problems []ComposeIssue }

// Error renders the problems as a Turkish message: a summary line followed
// by one "• " line per problem.
func (e *ComposeError) Error() string {
	lines := make([]string, 0, len(e.Problems)+1)
	lines = append(lines, fmt.Sprintf("Compose dosyası kabul edilmedi (%d sorun):", len(e.Problems)))
	for _, p := range e.Problems {
		lines = append(lines, "• "+p.Text())
	}
	return strings.Join(lines, "\n")
}

// ConvertOptions are the inputs of a conversion besides the file.
type ConvertOptions struct {
	// Name is the application name chosen by the user. When empty the
	// compose "name" is used.
	Name string
	// Roots are the allowed roots for bind mounts (files.allowed_roots).
	Roots []string
	// Protected are further directories that may never be mounted (the
	// panel's data directory).
	Protected []string
	// Taken, when set, returns a Turkish reason when slug is already used
	// by another application, or "".
	Taken func(slug string) string
}

// Conversion is the result of a successful conversion.
type Conversion struct {
	// Manifest is the validated manifest, parsed back from YAML.
	Manifest *Manifest
	// Notes explain what was changed on the way (ignored container names,
	// relative folders turned into Docker volumes, ...).
	Notes []ComposeIssue
	// YAML is the canonical manifest: exactly what is stored.
	YAML []byte
	// Digest is the SHA-256 of YAML, so that what is saved can be checked
	// to be what was previewed.
	Digest string
}

/* ---------- YAML tree with bounded alias expansion ---------- */

type ckind uint8

const (
	cNull ckind = iota
	cScalar
	cList
	cMap
)

// cnode is a YAML node after anchors, aliases and merge keys are resolved.
type cnode struct {
	kind   ckind
	value  string
	tag    string
	items  []*cnode
	keys   []string
	fields map[string]*cnode
}

func (n *cnode) get(k string) *cnode {
	if n == nil || n.kind != cMap {
		return nil
	}
	return n.fields[k]
}

var (
	errComposeTooComplex = errors.New("YAML çok karmaşık: takma adlar (alias) açıldığında izin verilen boyut aşılıyor")
	errComposeTooDeep    = errors.New("YAML çok derin iç içe geçmiş")
)

type treeBuilder struct{ nodes int }

// build converts a yaml.Node into a cnode. Aliases are expanded by walking
// the anchored node again; every node produced counts against
// maxComposeNodes, so an alias bomb stops after a bounded amount of work.
func (b *treeBuilder) build(n *yaml.Node, depth int) (*cnode, error) {
	if n == nil {
		return &cnode{kind: cNull}, nil
	}
	if depth > maxComposeDepth {
		return nil, errComposeTooDeep
	}
	b.nodes++
	if b.nodes > maxComposeNodes {
		return nil, errComposeTooComplex
	}
	switch n.Kind {
	case yaml.DocumentNode:
		if len(n.Content) == 0 {
			return &cnode{kind: cNull}, nil
		}
		return b.build(n.Content[0], depth+1)
	case yaml.AliasNode:
		return b.build(n.Alias, depth+1)
	case yaml.ScalarNode:
		tag := n.ShortTag()
		switch tag {
		case "!!null":
			return &cnode{kind: cNull}, nil
		case "!!str", "!!int", "!!float", "!!bool", "!!timestamp":
			return &cnode{kind: cScalar, value: n.Value, tag: tag}, nil
		}
		return nil, fmt.Errorf("satır %d: %s YAML etiketi desteklenmez", n.Line, tag)
	case yaml.SequenceNode:
		out := &cnode{kind: cList}
		for _, c := range n.Content {
			x, err := b.build(c, depth+1)
			if err != nil {
				return nil, err
			}
			out.items = append(out.items, x)
		}
		return out, nil
	case yaml.MappingNode:
		out := &cnode{kind: cMap, fields: map[string]*cnode{}}
		var merges []*cnode
		for i := 0; i+1 < len(n.Content); i += 2 {
			k, v := n.Content[i], n.Content[i+1]
			if k.Kind == yaml.ScalarNode && k.ShortTag() == "!!merge" {
				m, err := b.build(v, depth+1)
				if err != nil {
					return nil, err
				}
				switch m.kind {
				case cMap:
					merges = append(merges, m)
				case cList:
					for _, it := range m.items {
						if it.kind != cMap {
							return nil, fmt.Errorf("satır %d: birleştirme anahtarı (<<) yalnızca eşlemelerle kullanılabilir", k.Line)
						}
						merges = append(merges, it)
					}
				default:
					return nil, fmt.Errorf("satır %d: birleştirme anahtarı (<<) yalnızca eşlemelerle kullanılabilir", k.Line)
				}
				continue
			}
			key := k
			if key.Kind == yaml.AliasNode && key.Alias != nil {
				key = key.Alias
			}
			if key.Kind != yaml.ScalarNode {
				return nil, fmt.Errorf("satır %d: eşleme anahtarları düz metin olmalıdır", k.Line)
			}
			if _, dup := out.fields[key.Value]; dup {
				return nil, fmt.Errorf("satır %d: %q anahtarı birden fazla kez yazılmış", k.Line, key.Value)
			}
			x, err := b.build(v, depth+1)
			if err != nil {
				return nil, err
			}
			out.keys = append(out.keys, key.Value)
			out.fields[key.Value] = x
		}
		// Explicit keys win over merged ones; of several merged maps the
		// first one wins (YAML merge key semantics).
		for _, m := range merges {
			for _, key := range m.keys {
				if _, ok := out.fields[key]; !ok {
					out.keys = append(out.keys, key)
					out.fields[key] = m.fields[key]
				}
			}
		}
		return out, nil
	}
	return nil, errors.New("YAML düğümü anlaşılamadı")
}

var yamlLineRe = regexp.MustCompile(`line (\d+)`)

// parseComposeTree reads exactly one YAML document.
func parseComposeTree(src []byte) (*cnode, error) {
	dec := yaml.NewDecoder(bytes.NewReader(src))
	var doc yaml.Node
	if err := dec.Decode(&doc); err != nil {
		if errors.Is(err, io.EOF) {
			return nil, errors.New("compose dosyası boş")
		}
		if m := yamlLineRe.FindStringSubmatch(err.Error()); m != nil {
			return nil, fmt.Errorf("YAML sözdizimi hatalı (satır %s)", m[1])
		}
		return nil, errors.New("YAML sözdizimi hatalı")
	}
	var extra yaml.Node
	if err := dec.Decode(&extra); err == nil {
		return nil, errors.New("dosyada birden fazla YAML belgesi (---) var; yalnızca bir belge kabul edilir")
	} else if !errors.Is(err, io.EOF) {
		return nil, errors.New("YAML sözdizimi hatalı")
	}
	b := &treeBuilder{}
	return b.build(&doc, 0)
}

/* ---------- interpolation ---------- */

var varNameRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// varRef is a ${NAME}, ${NAME:-default} or ${NAME:?message} reference.
type varRef struct {
	Name     string
	Default  string
	Required bool
}

// parseInterp examines a compose string. It returns the literal text ($$
// unescaped) when s contains no variable, or the reference when s is exactly
// one variable. mixed is true when variables are combined with other text
// or with each other, which the manifest cannot express.
func parseInterp(s string) (lit string, ref *varRef, mixed bool, err error) {
	var b strings.Builder
	var refs []varRef
	for i := 0; i < len(s); i++ {
		ch := s[i]
		if ch != '$' {
			b.WriteByte(ch)
			continue
		}
		if i+1 < len(s) && s[i+1] == '$' {
			b.WriteByte('$')
			i++
			continue
		}
		if i+1 < len(s) && s[i+1] == '{' {
			end := strings.IndexByte(s[i+2:], '}')
			if end < 0 {
				return "", nil, false, errors.New("kapanmayan ${ ifadesi")
			}
			body := s[i+2 : i+2+end]
			r, perr := parseVarBody(body)
			if perr != nil {
				return "", nil, false, perr
			}
			refs = append(refs, r)
			i += 2 + end
			continue
		}
		if i+1 < len(s) && (s[i+1] == '_' || isASCIILetter(s[i+1])) {
			j := i + 1
			for j < len(s) && (s[j] == '_' || isASCIILetter(s[j]) || (s[j] >= '0' && s[j] <= '9')) {
				j++
			}
			refs = append(refs, varRef{Name: s[i+1 : j]})
			i = j - 1
			continue
		}
		b.WriteByte('$')
	}
	switch {
	case len(refs) == 0:
		return b.String(), nil, false, nil
	case len(refs) == 1 && b.Len() == 0:
		return "", &refs[0], false, nil
	}
	return "", nil, true, nil
}

func isASCIILetter(c byte) bool { return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') }

func parseVarBody(body string) (varRef, error) {
	name := body
	op, rest := "", ""
	for _, o := range []string{":-", ":?", ":+", "-", "?", "+"} {
		if i := strings.Index(body, o); i > 0 {
			if op == "" || i < len(name) {
				name, op, rest = body[:i], o, body[i+len(o):]
			}
		}
	}
	if !varNameRe.MatchString(name) {
		return varRef{}, fmt.Errorf("değişken adı %q geçersiz", name)
	}
	if strings.ContainsAny(rest, "${}") {
		return varRef{}, fmt.Errorf("${%s} içinde iç içe değişken desteklenmez", name)
	}
	switch op {
	case "":
		return varRef{Name: name}, nil
	case ":-", "-":
		return varRef{Name: name, Default: rest}, nil
	case ":?", "?":
		return varRef{Name: name, Required: true}, nil
	}
	return varRef{}, fmt.Errorf("${%s%s...} (alternatif değer) biçimi desteklenmez", name, op)
}

/* ---------- conversion ---------- */

// refused explains, in Turkish, why a compose key is not supported.
var refusedServiceKeys = map[string]string{
	"build":               "Panel görüntü derlemez; yalnızca hazır bir görüntü (image) kullanılabilir. Görüntüyü bir kayıt deposuna gönderip image alanını kullanın.",
	"env_file":            "Ortam dosyaları okunamaz; değişkenleri environment altına yazın.",
	"entrypoint":          "Giriş noktası (entrypoint) değiştirilemez; görüntünün kendi giriş noktası kullanılır. Gerekirse command kullanın.",
	"configs":             "Docker config nesneleri desteklenmez; değerleri ortam değişkeni veya birim olarak verin.",
	"secrets":             "Docker secret nesneleri desteklenmez; değerleri ortam değişkeni olarak verin.",
	"extends":             "Başka bir tanımdan miras alma (extends) desteklenmez; tanımı tek dosyada birleştirin.",
	"profiles":            "Profiller desteklenmez; kurulacak servisleri dosyada bırakıp diğerlerini silin.",
	"deploy":              "Swarm ve kaynak sınırı ayarları (deploy) desteklenmez.",
	"pid":                 "Sunucunun süreç ad alanını paylaşmak konteyneri yalıtımdan çıkarır; desteklenmez.",
	"ipc":                 "Paylaşılan IPC ad alanı konteyneri yalıtımdan çıkarır; desteklenmez.",
	"uts":                 "Sunucunun UTS ad alanını paylaşmak desteklenmez.",
	"userns_mode":         "Kullanıcı ad alanı ayarı desteklenmez.",
	"cgroup_parent":       "cgroup ayarları desteklenmez.",
	"cgroup":              "cgroup ayarları desteklenmez.",
	"security_opt":        "Güvenlik profillerini (seccomp, AppArmor) değiştirmek desteklenmez.",
	"sysctls":             "Çekirdek ayarları (sysctls) desteklenmez.",
	"volumes_from":        "Başka bir konteynerin birimlerini devralmak desteklenmez; ortak bir adlandırılmış birim kullanın.",
	"links":               "links eskidir ve gerekmez: servisler birbirine servis adıyla ulaşır. Bu satırı silin.",
	"external_links":      "Uygulama dışındaki konteynerlere bağlantı desteklenmez.",
	"extra_hosts":         "Konteynerin hosts dosyasına kayıt eklemek desteklenmez.",
	"labels":              "Konteyner etiketlerini panel yönetir; özel etiketler desteklenmez.",
	"cap_drop":            "Yetki kaldırma (cap_drop) desteklenmez.",
	"hostname":            "Konteyner ana makine adı değiştirilemez; servis adı kullanılır.",
	"domainname":          "Alan adı ayarı desteklenmez.",
	"working_dir":         "Çalışma klasörü değiştirilemez; görüntünün kendi ayarı kullanılır.",
	"dns":                 "DNS sunucusu ayarı desteklenmez.",
	"dns_search":          "DNS arama alanı ayarı desteklenmez.",
	"dns_opt":             "DNS seçenekleri desteklenmez.",
	"logging":             "Günlük sürücüsü ayarları desteklenmez; loglar panelden izlenir.",
	"ulimits":             "ulimit ayarları desteklenmez.",
	"runtime":             "Farklı bir konteyner çalışma zamanı seçmek desteklenmez.",
	"isolation":           "Yalıtım teknolojisi seçimi desteklenmez.",
	"platform":            "Platform seçimi desteklenmez; sunucunun mimarisine uygun görüntü indirilir.",
	"pull_policy":         "Görüntü indirme kuralı desteklenmez; güncelleme panelden yapılır.",
	"read_only":           "Salt okunur kök dosya sistemi ayarı desteklenmez.",
	"group_add":           "Ek grup üyeliği desteklenmez.",
	"init":                "init ayarı desteklenmez.",
	"tty":                 "tty ayarı desteklenmez.",
	"stdin_open":          "stdin_open ayarı desteklenmez.",
	"stop_signal":         "Durdurma sinyali ayarı desteklenmez.",
	"stop_grace_period":   "Durdurma bekleme süresi ayarı desteklenmez.",
	"mem_limit":           "Kaynak sınırları desteklenmez.",
	"mem_reservation":     "Kaynak sınırları desteklenmez.",
	"memswap_limit":       "Kaynak sınırları desteklenmez.",
	"cpus":                "Kaynak sınırları desteklenmez.",
	"cpu_shares":          "Kaynak sınırları desteklenmez.",
	"cpuset":              "Kaynak sınırları desteklenmez.",
	"oom_kill_disable":    "Bellek yetersizliği ayarları desteklenmez.",
	"oom_score_adj":       "Bellek yetersizliği ayarları desteklenmez.",
	"gpus":                "GPU ataması desteklenmez; gerekirse devices ile aygıt verin.",
	"device_cgroup_rules": "Aygıt cgroup kuralları desteklenmez; devices kullanın.",
	"mac_address":         "MAC adresi ayarı desteklenmez.",
	"storage_opt":         "Depolama seçenekleri desteklenmez.",
	"blkio_config":        "Disk G/Ç ayarları desteklenmez.",
	"annotations":         "Açıklama (annotation) ayarları desteklenmez.",
	"develop":             "Geliştirme (watch) ayarları desteklenmez.",
	"post_start":          "Yaşam döngüsü kancaları desteklenmez.",
	"pre_stop":            "Yaşam döngüsü kancaları desteklenmez.",
	"scale":               "Birden fazla kopya çalıştırmak desteklenmez.",
	"credential_spec":     "Kimlik bilgisi ayarı desteklenmez.",
	"attach":              "attach ayarı desteklenmez.",
}

var refusedTopKeys = map[string]string{
	"configs": "Docker config nesneleri desteklenmez.",
	"secrets": "Docker secret nesneleri desteklenmez; değerleri ortam değişkeni olarak verin.",
	"include": "Başka dosyaları içe aktarmak (include) desteklenmez; tanımı tek dosyada birleştirin.",
	"models":  "models desteklenmez.",
}

const genericRefusal = "Bu anahtar desteklenmiyor."

type converter struct {
	opt      ConvertOptions
	problems []ComposeIssue
	notes    []ComposeIssue
	volumes  map[string]bool // declared top-level named volumes
	networks map[string]bool // declared top-level networks
	envVars  map[string]varRef
	loopback bool
	ports    int
	vols     int
	envs     int
	portKeys map[string]bool
	volNames map[string]bool
}

func (c *converter) fail(service, key, format string, args ...any) {
	if len(c.problems) >= maxComposeProblems {
		return
	}
	c.problems = append(c.problems, ComposeIssue{Service: service, Key: key, Message: fmt.Sprintf(format, args...)})
}

func (c *converter) note(service, key, format string, args ...any) {
	c.notes = append(c.notes, ComposeIssue{Service: service, Key: key, Message: fmt.Sprintf(format, args...)})
}

// text returns a scalar value in which variables are not allowed.
func (c *converter) text(n *cnode, service, key string) (string, bool) {
	if n == nil || n.kind != cScalar {
		c.fail(service, key, "bir metin değeri bekleniyor")
		return "", false
	}
	lit, ref, mixed, err := parseInterp(n.value)
	switch {
	case err != nil:
		c.fail(service, key, "%s", err.Error())
		return "", false
	case ref != nil || mixed:
		c.fail(service, key, "değişkenler (${...}) yalnızca bir ortam değişkeninin değerinin tamamı olarak kullanılabilir")
		return "", false
	}
	return lit, true
}

func (c *converter) boolean(n *cnode, service, key string) (bool, bool) {
	if n == nil || n.kind != cScalar {
		c.fail(service, key, "true veya false bekleniyor")
		return false, false
	}
	switch strings.ToLower(n.value) {
	case "true":
		return true, true
	case "false":
		return false, true
	}
	c.fail(service, key, "true veya false bekleniyor")
	return false, false
}

// stringList accepts a list of scalars (variables not allowed).
func (c *converter) stringList(n *cnode, service, key string) ([]string, bool) {
	if n == nil || n.kind != cList {
		c.fail(service, key, "bir liste bekleniyor")
		return nil, false
	}
	out := make([]string, 0, len(n.items))
	ok := true
	for _, it := range n.items {
		s, good := c.text(it, service, key)
		if !good {
			ok = false
			continue
		}
		out = append(out, s)
	}
	return out, ok
}

// ConvertCompose converts a compose file into a validated manifest. It
// returns a *ComposeError listing every problem when the file is refused.
func ConvertCompose(src []byte, opt ConvertOptions) (*Conversion, error) {
	c := &converter{
		opt: opt, volumes: map[string]bool{}, networks: map[string]bool{},
		envVars: map[string]varRef{}, portKeys: map[string]bool{}, volNames: map[string]bool{},
	}
	man := c.convert(src)
	if len(c.problems) > 0 {
		return nil, &ComposeError{Problems: c.problems}
	}
	refused := func(err error) error {
		return &ComposeError{Problems: []ComposeIssue{{Message: "Dönüştürülen uygulama tanımı geçersiz: " + err.Error()}}}
	}
	first, err := manifestYAML(man)
	if err != nil {
		return nil, err
	}
	normalized, err := Parse(first)
	if err != nil {
		return nil, refused(err)
	}
	// The stored form is rendered from the normalized manifest, so that it
	// reads back into exactly the same manifest. It is validated again, by
	// the same strict validation as a shipped manifest, on exactly the
	// bytes that will be stored.
	data, err := manifestYAML(normalized)
	if err != nil {
		return nil, err
	}
	parsed, err := Parse(data)
	if err != nil {
		return nil, refused(err)
	}
	sum := sha256.Sum256(data)
	notes := c.notes
	if notes == nil {
		notes = []ComposeIssue{}
	}
	return &Conversion{Manifest: parsed, Notes: notes, YAML: data, Digest: hex.EncodeToString(sum[:])}, nil
}

func (c *converter) convert(src []byte) *Manifest {
	if len(src) > MaxComposeBytes {
		c.fail("", "", "compose dosyası çok büyük (en fazla %d KB)", MaxComposeBytes>>10)
		return nil
	}
	if !textOK(strings.ReplaceAll(string(src), "\r", ""), MaxComposeBytes, true) {
		c.fail("", "", "compose dosyası denetim karakterleri içeriyor")
		return nil
	}
	root, err := parseComposeTree(src)
	if err != nil {
		c.fail("", "", "%s", err.Error())
		return nil
	}
	if root.kind != cMap {
		c.fail("", "", "compose dosyasının en üst düzeyi bir eşleme (services: ...) olmalıdır")
		return nil
	}

	composeName := ""
	for _, k := range root.keys {
		v := root.fields[k]
		switch {
		case strings.HasPrefix(k, "x-"):
			// Extension fields hold anchors for reuse; compose ignores them.
		case k == "services":
		case k == "version":
			c.note("", "version", "version alanı eskidir ve yok sayıldı.")
		case k == "name":
			if s, ok := c.text(v, "", "name"); ok {
				composeName = strings.TrimSpace(s)
			}
		case k == "volumes":
			c.topVolumes(v)
		case k == "networks":
			c.topNetworks(v)
		default:
			msg, ok := refusedTopKeys[k]
			if !ok {
				msg = genericRefusal
			}
			c.fail("", k, "%s", msg)
		}
	}

	name := strings.TrimSpace(c.opt.Name)
	if name == "" {
		name = composeName
	}
	slug := ""
	switch {
	case name == "":
		c.fail("", "", "Uygulama adı zorunludur.")
	case !textOK(name, 60, false):
		c.fail("", "", "Uygulama adı en fazla 60 karakter olabilir ve denetim karakteri içeremez.")
	default:
		slug = customSlug(name)
		if slug == "" {
			c.fail("", "", "Uygulama adından geçerli bir kısa ad üretilemedi; harf veya rakam içeren bir ad seçin.")
		} else if c.opt.Taken != nil {
			if reason := c.opt.Taken(slug); reason != "" {
				c.fail("", "", "%s", reason)
			}
		}
	}

	svcNode := root.get("services")
	if svcNode == nil || svcNode.kind != cMap || len(svcNode.keys) == 0 {
		c.fail("", "services", "en az bir servis tanımlanmalıdır")
		return nil
	}
	if len(svcNode.keys) > maxComposeServices {
		c.fail("", "services", "en fazla %d servis tanımlanabilir (dosyada %d var)", maxComposeServices, len(svcNode.keys))
		return nil
	}
	names := map[string]bool{}
	for _, n := range svcNode.keys {
		names[n] = true
	}
	services := make([]ServiceSpec, 0, len(svcNode.keys))
	for _, n := range svcNode.keys {
		services = append(services, c.service(n, svcNode.fields[n], names))
	}
	if c.ports > maxComposePorts {
		c.fail("", "ports", "en fazla %d port yayınlanabilir", maxComposePorts)
	}
	if c.vols > maxComposeVolumes {
		c.fail("", "volumes", "en fazla %d birim bağlanabilir", maxComposeVolumes)
	}
	if c.envs > maxComposeEnv {
		c.fail("", "environment", "en fazla %d ortam değişkeni tanımlanabilir", maxComposeEnv)
	}
	if len(c.problems) == 0 {
		if _, err := StartOrder(services); err != nil {
			c.fail("", "depends_on", "%s", err.Error())
		}
	}
	c.checkHostPorts(services)
	c.pickWebUI(services)

	m := &Manifest{
		Schema: SchemaVersion, Name: name, Slug: slug, Description: customDescription,
		Category: CustomCategory, Icon: CustomIcon, Services: services,
		LongDescription: "Bu uygulama yönetici tarafından bir docker-compose dosyasından eklendi. Görüntüler: " + imageList(services) + ".",
	}
	if c.loopback {
		m.BindAddress = BindLoopback
		c.note("", "ports", "Compose dosyası portları 127.0.0.1 adresinde yayınlıyor; kurulumda \"Yalnızca bu sunucudan erişilsin\" seçeneği açık gelir ve tüm portlar için geçerlidir.")
	}
	// The notes are also shown in the install dialog. They are generated
	// text; what does not fit the manifest's limit is left out there but
	// still returned by the preview.
	var notes strings.Builder
	for _, n := range c.notes {
		line := "• " + n.Text()
		if !textOK(line, 1000, false) {
			continue
		}
		if notes.Len()+len(line)+1 > 4000 {
			break
		}
		if notes.Len() > 0 {
			notes.WriteByte('\n')
		}
		notes.WriteString(line)
	}
	m.Notes = notes.String()
	return m
}

func imageList(services []ServiceSpec) string {
	seen := map[string]bool{}
	var out []string
	for _, s := range services {
		if s.Image != "" && !seen[s.Image] {
			seen[s.Image] = true
			out = append(out, s.Image)
		}
	}
	s := strings.Join(out, ", ")
	if len(s) > 3000 {
		s = s[:3000]
	}
	return s
}

func (c *converter) topVolumes(n *cnode) {
	if n.kind == cNull {
		return
	}
	if n.kind != cMap {
		c.fail("", "volumes", "birim tanımları bir eşleme olmalıdır")
		return
	}
	for _, name := range n.keys {
		key := "volumes." + name
		if !volNameRe.MatchString(name) {
			c.fail("", key, "birim adı geçersiz (küçük harf, rakam, '-', '_' ve '.')")
			continue
		}
		c.volumes[name] = true
		def := n.fields[name]
		if def.kind == cNull {
			continue
		}
		if def.kind != cMap {
			c.fail("", key, "birim tanımı anlaşılamadı")
			continue
		}
		for _, k := range def.keys {
			v := def.fields[k]
			switch k {
			case "external":
				if b, ok := c.boolean(v, "", key+".external"); ok && b {
					c.fail("", key+".external", "Dışarıda oluşturulmuş birimler kullanılamaz; panel yalnızca bu uygulama için kendi oluşturduğu birimleri bağlar.")
				}
			case "driver":
				if s, ok := c.text(v, "", key+".driver"); ok && s != "local" {
					c.fail("", key+".driver", "Yalnızca yerel (local) birim sürücüsü desteklenir.")
				}
			case "driver_opts":
				c.fail("", key+".driver_opts", "Birim sürücüsü seçenekleri desteklenmez; bu seçenekler sunucudaki herhangi bir klasörü bağlamak için kullanılabilir.")
			case "name":
				c.fail("", key+".name", "Birimin Docker adı değiştirilemez; panel birimi myserver-<uygulama>-<ad> olarak adlandırır.")
			default:
				c.fail("", key+"."+k, "%s", genericRefusal)
			}
		}
	}
}

func (c *converter) topNetworks(n *cnode) {
	if n.kind == cNull {
		return
	}
	if n.kind != cMap {
		c.fail("", "networks", "ağ tanımları bir eşleme olmalıdır")
		return
	}
	for _, name := range n.keys {
		def := n.fields[name]
		if def.kind == cNull || (def.kind == cMap && len(def.keys) == 0) {
			c.networks[name] = true
			continue
		}
		c.fail("", "networks."+name, "Özel ağ tanımları (sürücü, dış ağ, IP ayarları) desteklenmez; uygulamanın tüm servisleri tek bir özel ağda birbirine servis adıyla ulaşır.")
	}
}

func (c *converter) service(name string, n *cnode, names map[string]bool) ServiceSpec {
	s := ServiceSpec{Name: name}
	if !serviceRe.MatchString(name) {
		c.fail(name, "", "servis adı yalnızca küçük harf, rakam ve '-' içerebilir (en fazla 32 karakter); servis adı diğer servislerin ona ulaştığı DNS adıdır")
	}
	if n == nil || n.kind != cMap {
		c.fail(name, "", "servis tanımı bir eşleme olmalıdır")
		return s
	}
	if n.get("build") != nil {
		c.fail(name, "build", "%s", refusedServiceKeys["build"])
	} else if n.get("image") == nil {
		c.fail(name, "image", "image zorunludur; panel yalnızca hazır görüntülerle çalışır")
	}
	for _, k := range n.keys {
		v := n.fields[k]
		switch k {
		case "build":
			// Reported above.
		case "image":
			if img, ok := c.text(v, name, "image"); ok {
				if !ValidImage(img) {
					c.fail(name, "image", "%q geçerli bir görüntü adı değil (örnek: ghcr.io/kurum/uygulama:1.2)", img)
				}
				s.Image = img
			}
		case "container_name":
			c.note(name, "container_name", "yok sayıldı; panel konteyneri kendisi adlandırır (myserver-<uygulama>).")
		case "expose":
			c.note(name, "expose", "yok sayıldı; servisler uygulamanın özel ağında birbirinin tüm portlarına zaten ulaşabilir.")
		case "ports":
			c.servicePorts(&s, v)
		case "volumes":
			c.serviceVolumes(&s, v)
		case "environment":
			c.serviceEnv(&s, v)
		case "command":
			c.serviceCommand(&s, v)
		case "user":
			if u, ok := c.text(v, name, "user"); ok {
				if !userRe.MatchString(u) {
					c.fail(name, "user", "%q geçersiz (kullanıcı veya kullanıcı:grup; ad ya da sayı)", u)
				}
				s.User = u
			}
		case "restart":
			if r, ok := c.text(v, name, "restart"); ok {
				switch r {
				case "no", "always", "unless-stopped", "on-failure":
					s.Restart = r
				default:
					c.fail(name, "restart", "%q desteklenmez (no, always, unless-stopped, on-failure)", r)
				}
			}
		case "depends_on":
			c.serviceDepends(&s, v, names)
		case "healthcheck":
			c.serviceHealth(&s, v)
		case "cap_add":
			if list, ok := c.stringList(v, name, "cap_add"); ok {
				for _, cp := range list {
					cp = strings.TrimPrefix(strings.ToUpper(strings.TrimSpace(cp)), "CAP_")
					if cp == "ALL" {
						c.fail(name, "cap_add", "ALL (tüm yetkiler) desteklenmez; gereken yetkileri tek tek yazın")
						continue
					}
					if !knownCaps[cp] {
						c.fail(name, "cap_add", "%q bilinmeyen bir yetki", cp)
						continue
					}
					if !contains(s.CapAdd, cp) {
						s.CapAdd = append(s.CapAdd, cp)
					}
				}
			}
		case "devices":
			c.serviceDevices(&s, v)
		case "privileged":
			if b, ok := c.boolean(v, name, "privileged"); ok {
				s.Privileged = b
			}
		case "network_mode":
			if nm, ok := c.text(v, name, "network_mode"); ok {
				switch nm {
				case "host", "none", "bridge":
					s.NetworkMode = nm
				default:
					c.fail(name, "network_mode", "%q desteklenmez; yalnızca host, bridge veya none kullanılabilir", nm)
				}
			}
		case "networks":
			c.serviceNetworks(name, v)
		case "shm_size":
			if sz, ok := c.size(v, name, "shm_size"); ok {
				s.ShmSize = sz
			}
		case "tmpfs":
			c.serviceTmpfs(&s, v)
		default:
			if strings.HasPrefix(k, "x-") {
				continue
			}
			msg, ok := refusedServiceKeys[k]
			if !ok {
				msg = genericRefusal
			}
			c.fail(name, k, "%s", msg)
		}
	}
	return s
}

func (c *converter) serviceNetworks(service string, n *cnode) {
	var list []string
	switch n.kind {
	case cList:
		for _, it := range n.items {
			if s, ok := c.text(it, service, "networks"); ok {
				list = append(list, s)
			}
		}
	case cMap:
		for _, k := range n.keys {
			v := n.fields[k]
			if v.kind != cNull && !(v.kind == cMap && len(v.keys) == 0) {
				c.fail(service, "networks."+k, "Ağ ayarları (takma ad, sabit IP) desteklenmez.")
				continue
			}
			list = append(list, k)
		}
	default:
		c.fail(service, "networks", "ağ listesi anlaşılamadı")
		return
	}
	for _, name := range list {
		if name != "default" && !c.networks[name] {
			c.fail(service, "networks", "%q ağı en üst düzeyde tanımlı değil", name)
		}
	}
	if len(list) > 0 {
		c.note(service, "networks", "servis uygulamanın tek özel ağına bağlanır; diğer servislere servis adıyla ulaşır.")
	}
}

/* ---------- ports ---------- */

func (c *converter) servicePorts(s *ServiceSpec, n *cnode) {
	if n.kind != cList {
		c.fail(s.Name, "ports", "port listesi bekleniyor")
		return
	}
	for _, it := range n.items {
		c.ports++
		var p PortSpec
		ok := false
		switch it.kind {
		case cScalar:
			p, ok = c.shortPort(s.Name, it)
		case cMap:
			p, ok = c.longPort(s.Name, it)
		default:
			c.fail(s.Name, "ports", "port tanımı anlaşılamadı")
		}
		if !ok {
			continue
		}
		base := fmt.Sprintf("%s-%d-%s", s.Name, p.Container, p.Protocol)
		key := base
		for i := 2; c.portKeys[key]; i++ {
			key = fmt.Sprintf("%s-%d", base, i)
		}
		c.portKeys[key] = true
		p.Key = key
		s.Ports = append(s.Ports, p)
	}
}

// hostIP maps a compose host address to the bind choice; loopback is
// remembered for the whole application.
func (c *converter) hostIP(service, ip string) bool {
	switch ip {
	case "", "0.0.0.0", "::":
		return true
	case "127.0.0.1":
		c.loopback = true
		return true
	}
	c.fail(service, "ports", "%s adresinde yayın desteklenmez; yalnızca 0.0.0.0 (tüm arayüzler) veya 127.0.0.1 (yalnızca bu sunucu) kullanılabilir", ip)
	return false
}

func parsePortNumber(s string) (int, bool) {
	n, err := strconv.Atoi(s)
	if err != nil || !validPort(n) || strconv.Itoa(n) != s {
		return 0, false
	}
	return n, true
}

func (c *converter) shortPort(service string, it *cnode) (PortSpec, bool) {
	raw, ok := c.text(it, service, "ports")
	if !ok {
		return PortSpec{}, false
	}
	spec := strings.TrimSpace(raw)
	proto := "tcp"
	if i := strings.LastIndexByte(spec, '/'); i >= 0 {
		proto = strings.ToLower(spec[i+1:])
		spec = spec[:i]
	}
	if proto != "tcp" && proto != "udp" {
		c.fail(service, "ports", "%q: protokol %q desteklenmez (tcp, udp)", raw, proto)
		return PortSpec{}, false
	}
	if strings.HasPrefix(spec, "[") {
		c.fail(service, "ports", "%q: IPv6 adresinde yayın desteklenmez", raw)
		return PortSpec{}, false
	}
	parts := strings.Split(spec, ":")
	var ip, host, cont string
	switch len(parts) {
	case 1:
		cont = parts[0]
	case 2:
		host, cont = parts[0], parts[1]
	case 3:
		ip, host, cont = parts[0], parts[1], parts[2]
	default:
		c.fail(service, "ports", "%q anlaşılamadı (örnek: 8080:80, 127.0.0.1:8080:80, 53:53/udp)", raw)
		return PortSpec{}, false
	}
	if strings.Contains(host, "-") || strings.Contains(cont, "-") {
		c.fail(service, "ports", "%q: port aralıkları desteklenmez; her portu ayrı satırda yazın", raw)
		return PortSpec{}, false
	}
	if !c.hostIP(service, ip) {
		return PortSpec{}, false
	}
	cn, ok := parsePortNumber(cont)
	if !ok {
		c.fail(service, "ports", "%q: konteyner portu geçersiz (1-65535)", raw)
		return PortSpec{}, false
	}
	hn := cn
	if host != "" {
		if hn, ok = parsePortNumber(host); !ok {
			c.fail(service, "ports", "%q: sunucu portu geçersiz (1-65535)", raw)
			return PortSpec{}, false
		}
	} else {
		c.note(service, "ports", "%q için sunucu portu belirtilmemiş; varsayılan olarak %d önerilir, kurulumda değiştirebilirsiniz.", raw, cn)
	}
	return PortSpec{Host: hn, Container: cn, Protocol: proto}, true
}

func (c *converter) longPort(service string, it *cnode) (PortSpec, bool) {
	p := PortSpec{Protocol: "tcp"}
	ok := true
	for _, k := range it.keys {
		v := it.fields[k]
		key := "ports." + k
		switch k {
		case "target":
			s, good := c.text(v, service, key)
			if !good {
				ok = false
				continue
			}
			n, good := parsePortNumber(s)
			if !good {
				c.fail(service, key, "konteyner portu geçersiz (1-65535)")
				ok = false
				continue
			}
			p.Container = n
		case "published":
			s, good := c.text(v, service, key)
			if !good {
				ok = false
				continue
			}
			if strings.Contains(s, "-") {
				c.fail(service, key, "port aralıkları desteklenmez")
				ok = false
				continue
			}
			n, good := parsePortNumber(s)
			if !good {
				c.fail(service, key, "sunucu portu geçersiz (1-65535)")
				ok = false
				continue
			}
			p.Host = n
		case "protocol":
			s, good := c.text(v, service, key)
			s = strings.ToLower(s)
			if !good || (s != "tcp" && s != "udp") {
				c.fail(service, key, "yalnızca tcp veya udp")
				ok = false
				continue
			}
			p.Protocol = s
		case "host_ip":
			s, good := c.text(v, service, key)
			if !good || !c.hostIP(service, s) {
				ok = false
			}
		case "name":
			s, good := c.text(v, service, key)
			switch {
			case !good:
				ok = false
			case !textOK(s, 80, false):
				c.fail(service, key, "port adı en fazla 80 karakter olabilir")
				ok = false
			default:
				p.Label = s
			}
		case "mode":
			if s, good := c.text(v, service, key); good && s != "ingress" && s != "host" {
				c.fail(service, key, "%q bilinmiyor", s)
				ok = false
			} else if good {
				c.note(service, key, "yalnızca Swarm'da anlamlıdır ve yok sayıldı.")
			}
		default:
			c.fail(service, key, "%s", genericRefusal)
			ok = false
		}
	}
	if p.Container == 0 {
		if ok {
			c.fail(service, "ports.target", "konteyner portu (target) zorunludur")
		}
		return PortSpec{}, false
	}
	if p.Host == 0 {
		p.Host = p.Container
	}
	return p, ok
}

// checkHostPorts reports every host port used twice and the port rules of
// host networking, so that all such problems are listed together.
func (c *converter) checkHostPorts(services []ServiceSpec) {
	seen := map[string]string{}
	for _, s := range services {
		for _, p := range s.Ports {
			id := fmt.Sprintf("%d/%s", p.Host, p.Protocol)
			if other, dup := seen[id]; dup {
				c.fail(s.Name, "ports", "sunucu portu %s birden fazla kez kullanılmış (%s servisi de kullanıyor)", id, other)
				continue
			}
			seen[id] = s.Name
			if s.NetworkMode == "host" && p.Host != p.Container {
				c.fail(s.Name, "ports", "host ağ kipinde sunucu ve konteyner portu aynı olmalıdır (%d:%d)", p.Host, p.Container)
			}
			if s.NetworkMode == "none" {
				c.fail(s.Name, "ports", "ağı olmayan (network_mode: none) servis port yayınlayamaz")
			}
		}
	}
}

// pickWebUI marks the first TCP port of the application as its web
// interface, so that the panel can offer "Aç".
func (c *converter) pickWebUI(services []ServiceSpec) {
	for i := range services {
		for j := range services[i].Ports {
			p := &services[i].Ports[j]
			if p.Protocol != "tcp" {
				continue
			}
			scheme := "http"
			switch p.Container {
			case 443, 8443, 9443:
				scheme = "https"
			}
			p.WebUI = &WebUISpec{Scheme: scheme, Path: "/"}
			if p.Label == "" {
				p.Label = "Web arayüzü"
			}
			c.note(services[i].Name, "ports", "%d/tcp portu web arayüzü olarak kabul edildi (%s); \"Aç\" bağlantısı bu portu kullanır.", p.Host, scheme)
			return
		}
	}
}

/* ---------- volumes ---------- */

var fileLikeRe = regexp.MustCompile(`\.[A-Za-z0-9]{1,8}$`)

// isDockerSocket reports the only system path a custom application may
// mount (as a declared risk).
func isDockerSocket(p string) bool { return p == "/var/run/docker.sock" || p == "/run/docker.sock" }

func (c *converter) serviceVolumes(s *ServiceSpec, n *cnode) {
	if n.kind != cList {
		c.fail(s.Name, "volumes", "birim listesi bekleniyor")
		return
	}
	for _, it := range n.items {
		c.vols++
		var src, target string
		var readOnly, ok bool
		kind := ""
		switch it.kind {
		case cScalar:
			src, target, readOnly, ok = c.shortVolume(s.Name, it)
		case cMap:
			kind, src, target, readOnly, ok = c.longVolume(s, it)
		default:
			c.fail(s.Name, "volumes", "birim tanımı anlaşılamadı")
		}
		if !ok {
			continue
		}
		if kind == "tmpfs" {
			continue // added to s.Tmpfs by longVolume
		}
		if !cleanAbs(target) || target == "/" {
			c.fail(s.Name, "volumes", "hedef %q mutlak ve düzgün bir konteyner yolu olmalıdır", target)
			continue
		}
		if v, ok := c.volume(s.Name, src, target, readOnly, kind); ok {
			s.Volumes = append(s.Volumes, v)
		}
	}
}

func (c *converter) shortVolume(service string, it *cnode) (src, target string, readOnly, ok bool) {
	raw, good := c.text(it, service, "volumes")
	if !good {
		return "", "", false, false
	}
	parts := strings.Split(raw, ":")
	switch len(parts) {
	case 1:
		return "", parts[0], false, true
	case 2, 3:
		src, target = parts[0], parts[1]
	default:
		c.fail(service, "volumes", "%q anlaşılamadı (örnek: veri:/data, /data/uygulama:/config:ro)", raw)
		return "", "", false, false
	}
	if len(parts) == 3 {
		for _, opt := range strings.Split(parts[2], ",") {
			switch opt {
			case "ro":
				readOnly = true
			case "rw":
			default:
				c.fail(service, "volumes", "%q: %q seçeneği desteklenmez (yalnızca ro veya rw)", raw, opt)
				return "", "", false, false
			}
		}
	}
	if src == "" {
		c.fail(service, "volumes", "%q: kaynak boş", raw)
		return "", "", false, false
	}
	return src, target, readOnly, true
}

func (c *converter) longVolume(s *ServiceSpec, it *cnode) (kind, src, target string, readOnly, ok bool) {
	ok = true
	kind = "volume"
	tmpfsSize := ""
	for _, k := range it.keys {
		v := it.fields[k]
		key := "volumes." + k
		switch k {
		case "type":
			t, good := c.text(v, s.Name, key)
			if !good {
				ok = false
				continue
			}
			switch t {
			case "volume", "bind", "tmpfs":
				kind = t
			default:
				c.fail(s.Name, key, "%q birim türü desteklenmez (volume, bind, tmpfs)", t)
				ok = false
			}
		case "source":
			if t, good := c.text(v, s.Name, key); good {
				src = t
			} else {
				ok = false
			}
		case "target":
			if t, good := c.text(v, s.Name, key); good {
				target = t
			} else {
				ok = false
			}
		case "read_only":
			if b, good := c.boolean(v, s.Name, key); good {
				readOnly = b
			} else {
				ok = false
			}
		case "tmpfs":
			if v.kind != cMap {
				c.fail(s.Name, key, "tmpfs ayarları anlaşılamadı")
				ok = false
				continue
			}
			for _, tk := range v.keys {
				if tk != "size" {
					c.fail(s.Name, key+"."+tk, "%s", genericRefusal)
					ok = false
					continue
				}
				if sz, good := c.size(v.fields[tk], s.Name, key+".size"); good {
					tmpfsSize = sz
				} else {
					ok = false
				}
			}
		case "bind", "volume", "consistency", "image":
			c.fail(s.Name, key, "Birim alt seçenekleri desteklenmez.")
			ok = false
		default:
			c.fail(s.Name, key, "%s", genericRefusal)
			ok = false
		}
	}
	if !ok {
		return "", "", "", false, false
	}
	if target == "" {
		c.fail(s.Name, "volumes.target", "hedef (target) zorunludur")
		return "", "", "", false, false
	}
	if kind == "tmpfs" {
		if !cleanAbs(target) || target == "/" {
			c.fail(s.Name, "volumes", "tmpfs hedefi %q geçersiz", target)
			return "", "", "", false, false
		}
		s.Tmpfs = append(s.Tmpfs, TmpfsSpec{Target: target, Size: tmpfsSize})
		return kind, "", target, false, true
	}
	if kind == "bind" && src == "" {
		c.fail(s.Name, "volumes.source", "bağlanacak klasör (source) zorunludur")
		return "", "", "", false, false
	}
	if kind == "volume" && src != "" && (strings.HasPrefix(src, "/") || strings.HasPrefix(src, ".") || strings.HasPrefix(src, "~")) {
		c.fail(s.Name, "volumes.source", "volume türünde kaynak bir birim adı olmalıdır, klasör yolu değil")
		return "", "", "", false, false
	}
	if kind == "bind" && !strings.HasPrefix(src, "/") && !strings.HasPrefix(src, ".") && !strings.HasPrefix(src, "~") {
		c.fail(s.Name, "volumes.source", "bind türünde kaynak bir klasör yolu olmalıdır")
		return "", "", "", false, false
	}
	return kind, src, target, readOnly, true
}

// volume maps one mount. src "" is an anonymous volume; "/..." a bind
// mount; "./..." a relative folder, which becomes a panel-managed named
// volume; anything else a named volume declared at the top level.
func (c *converter) volume(service, src, target string, readOnly bool, kind string) (VolumeSpec, bool) {
	derived := strings.Trim(strings.ReplaceAll(target, "/", "-"), "-")
	switch {
	case src == "":
		name := volumeNameFrom("anon-" + service + "-" + derived)
		if !c.claimVolume(service, name) {
			return VolumeSpec{}, false
		}
		c.note(service, "volumes", "%s için adsız birim, panelin yönettiği %q adlı kalıcı Docker birimine dönüştürüldü.", target, name)
		return VolumeSpec{Type: VolumeNamed, Source: name, Target: target, ReadOnly: readOnly}, true

	case strings.HasPrefix(src, "~"):
		c.fail(service, "volumes", "%q: ev klasörüne göre (~) yol desteklenmez; mutlak bir yol yazın", src)
		return VolumeSpec{}, false

	case strings.HasPrefix(src, "."):
		rel := path.Clean(src)
		if fileLikeRe.MatchString(path.Base(rel)) {
			c.fail(service, "volumes", "%q tek bir dosyaya benziyor; göreli dosya bağlama desteklenmez. Dosyayı izin verilen bir klasöre koyup mutlak yolla bağlayın.", src)
			return VolumeSpec{}, false
		}
		base := strings.Trim(strings.ReplaceAll(strings.ReplaceAll(rel, "..", ""), "/", "-"), "-.")
		if base == "" {
			base = "proje"
		}
		name := volumeNameFrom("yerel-" + base)
		if name == "" {
			c.fail(service, "volumes", "%q için birim adı üretilemedi", src)
			return VolumeSpec{}, false
		}
		if !c.claimSharedVolume(name) {
			c.fail(service, "volumes", "%q klasörünün dönüştürüleceği %q birim adı, en üst düzeyde tanımlı bir birimle çakışıyor", src, name)
			return VolumeSpec{}, false
		}
		c.note(service, "volumes", "%s göreli klasörü yerine panelin yönettiği %q Docker birimi kullanılır; birim boş başlar, bilgisayarınızdaki dosyalar kopyalanmaz.", src, name)
		return VolumeSpec{Type: VolumeNamed, Source: name, Target: target, ReadOnly: readOnly}, true

	case strings.HasPrefix(src, "/"):
		p := path.Clean(src)
		if !cleanAbs(p) {
			c.fail(service, "volumes", "%q geçerli bir klasör yolu değil", src)
			return VolumeSpec{}, false
		}
		if isDockerSocket(p) {
			return VolumeSpec{Type: VolumeSystem, Source: p, Target: target, ReadOnly: readOnly, Label: "Docker soketi"}, true
		}
		if err := CheckBindPath(p, c.opt.Roots); err != nil {
			msg := userMessage(err)
			if p == "/etc/localtime" || p == "/etc/timezone" {
				msg += " Saat dilimi için TZ ortam değişkenini (örnek: TZ=Europe/Istanbul) kullanın."
			}
			c.fail(service, "volumes", "%s", msg)
			return VolumeSpec{}, false
		}
		if err := checkProtected(p, c.opt.Protected); err != nil {
			c.fail(service, "volumes", "%s", userMessage(err))
			return VolumeSpec{}, false
		}
		key := bindKey(service, derived)
		if key == "" {
			c.fail(service, "volumes", "%s için klasör anahtarı üretilemedi", target)
			return VolumeSpec{}, false
		}
		return VolumeSpec{
			Type: VolumeBind, Key: key, Source: p, Target: target, ReadOnly: readOnly, Required: true,
			Label: "Klasör: " + truncate(target, 70),
		}, true
	}
	if kind == "bind" {
		c.fail(service, "volumes", "%q bir klasör yolu değil", src)
		return VolumeSpec{}, false
	}
	if !c.volumes[src] {
		c.fail(service, "volumes", "%q birimi en üst düzeydeki volumes altında tanımlı değil", src)
		return VolumeSpec{}, false
	}
	c.volNames[src] = true
	return VolumeSpec{Type: VolumeNamed, Source: src, Target: target, ReadOnly: readOnly}, true
}

// truncate shortens s to at most n bytes without splitting a character.
func truncate(s string, n int) string {
	r := []rune(s)
	for len(string(r)) > n {
		r = r[:len(r)-1]
	}
	return string(r)
}

// claimVolume reserves a generated volume name that must be unique.
func (c *converter) claimVolume(service, name string) bool {
	if name == "" || c.volNames[name] || c.volumes[name] {
		c.fail(service, "volumes", "%q birim adı başka bir birimle çakışıyor", name)
		return false
	}
	c.volNames[name] = true
	return true
}

// claimSharedVolume reserves the volume of a relative folder. The same
// folder mounted by several services is the same volume, as in compose.
func (c *converter) claimSharedVolume(name string) bool {
	if c.volumes[name] {
		return false
	}
	c.volNames[name] = true
	return true
}

var volCleanRe = regexp.MustCompile(`[^a-z0-9_.-]+`)

func volumeNameFrom(s string) string {
	s = volCleanRe.ReplaceAllString(strings.ToLower(s), "-")
	for strings.Contains(s, "--") {
		s = strings.ReplaceAll(s, "--", "-")
	}
	s = strings.Trim(s, "-._")
	if len(s) > 60 {
		s = strings.TrimRight(s[:60], "-._")
	}
	if !volNameRe.MatchString(s) {
		return ""
	}
	return s
}

var keyCleanRe = regexp.MustCompile(`[^A-Za-z0-9_.-]+`)

func bindKey(service, derived string) string {
	k := keyCleanRe.ReplaceAllString(service+"-"+derived, "-")
	k = strings.Trim(k, "-_.")
	if len(k) > 64 {
		k = strings.TrimRight(k[:64], "-_.")
	}
	if !keyRe.MatchString(k) {
		return ""
	}
	return k
}

/* ---------- environment ---------- */

var (
	secretVarRe   = regexp.MustCompile(`(?i)(PASSWORD|PASSWD|PASS|SECRET|TOKEN|API_?KEY|PRIVATE_?KEY)`)
	generateVarRe = regexp.MustCompile(`(?i)(PASSWORD|PASSWD|PASS|SECRET)`)
)

func (c *converter) serviceEnv(s *ServiceSpec, n *cnode) {
	type entry struct {
		name  string
		value *cnode // nil: no value given ("NAME" or "NAME:")
		text  string
		isStr bool
	}
	var entries []entry
	switch n.kind {
	case cList:
		for _, it := range n.items {
			if it.kind != cScalar {
				c.fail(s.Name, "environment", "liste öğeleri AD=değer biçiminde olmalıdır")
				continue
			}
			name, val, has := strings.Cut(it.value, "=")
			if has {
				entries = append(entries, entry{name: name, text: val, isStr: true})
			} else {
				entries = append(entries, entry{name: name})
			}
		}
	case cMap:
		for _, k := range n.keys {
			v := n.fields[k]
			switch v.kind {
			case cNull:
				entries = append(entries, entry{name: k})
			case cScalar:
				entries = append(entries, entry{name: k, text: v.value, isStr: true})
			default:
				c.fail(s.Name, "environment."+k, "değer düz metin olmalıdır")
			}
		}
	case cNull:
		return
	default:
		c.fail(s.Name, "environment", "liste veya eşleme bekleniyor")
		return
	}
	seen := map[string]bool{}
	for _, e := range entries {
		c.envs++
		key := "environment." + e.name
		if !envNameRe.MatchString(e.name) {
			c.fail(s.Name, key, "değişken adı geçersiz (harf, rakam ve _; harf ile başlamalı)")
			continue
		}
		if seen[e.name] {
			c.fail(s.Name, key, "birden fazla kez tanımlanmış")
			continue
		}
		seen[e.name] = true
		if !e.isStr {
			fieldKey := s.Name + "." + e.name
			if !keyRe.MatchString(fieldKey) {
				c.fail(s.Name, key, "değişken adı çok uzun")
				continue
			}
			s.Env = append(s.Env, EnvSpec{
				Name: e.name, Key: fieldKey, Label: e.name,
				Description: "Compose dosyasında değer verilmemiş; kurulumda girin.",
			})
			continue
		}
		lit, ref, mixed, err := parseInterp(e.text)
		switch {
		case err != nil:
			c.fail(s.Name, key, "%s", err.Error())
			continue
		case mixed:
			c.fail(s.Name, key, "değişkenler (${...}) başka metinle birlikte kullanılamaz; değerin tamamı tek bir ${DEĞİŞKEN} olmalıdır")
			continue
		}
		if ref == nil {
			if !validEnvValue(lit) {
				c.fail(s.Name, key, "değer çok uzun veya satır sonu içeriyor")
				continue
			}
			fieldKey := s.Name + "." + e.name
			if !keyRe.MatchString(fieldKey) {
				c.fail(s.Name, key, "değişken adı çok uzun")
				continue
			}
			s.Env = append(s.Env, EnvSpec{Name: e.name, Key: fieldKey, Label: e.name, Default: lit})
			continue
		}
		if !keyRe.MatchString(ref.Name) {
			c.fail(s.Name, key, "${%s} değişken adı çok uzun", ref.Name)
			continue
		}
		if !validEnvValue(ref.Default) {
			c.fail(s.Name, key, "${%s} varsayılan değeri geçersiz", ref.Name)
			continue
		}
		if prev, ok := c.envVars[ref.Name]; ok {
			if prev.Default != ref.Default {
				c.fail(s.Name, key, "${%s} değişkeni dosyada farklı varsayılan değerlerle kullanılmış", ref.Name)
				continue
			}
		} else {
			c.envVars[ref.Name] = *ref
		}
		spec := EnvSpec{
			Name: e.name, Key: ref.Name, Label: ref.Name, Default: ref.Default,
			Required:    ref.Required,
			Description: "Compose dosyasındaki ${" + ref.Name + "} değişkeni.",
		}
		if ref.Default == "" && secretVarRe.MatchString(ref.Name) {
			spec.Secret = true
			if generateVarRe.MatchString(ref.Name) {
				spec.Generate = "password"
			}
		}
		// Whether a variable is secret depends only on its name and on
		// its default, which must be the same everywhere (checked above),
		// so a variable shared by several services is consistently secret.
		s.Env = append(s.Env, spec)
	}
}

/* ---------- command, health, devices, sizes ---------- */

func (c *converter) serviceCommand(s *ServiceSpec, n *cnode) {
	switch n.kind {
	case cScalar:
		raw, ok := c.text(n, s.Name, "command")
		if !ok {
			return
		}
		args, err := splitCommand(raw)
		if err != nil {
			c.fail(s.Name, "command", "%s", err.Error())
			return
		}
		s.Command = args
	case cList:
		if list, ok := c.stringList(n, s.Name, "command"); ok {
			s.Command = list
		}
	case cNull:
	default:
		c.fail(s.Name, "command", "metin veya liste bekleniyor")
		return
	}
	if len(s.Command) > 64 {
		c.fail(s.Name, "command", "en fazla 64 argüman olabilir")
	}
	for _, a := range s.Command {
		if !validEnvValue(a) {
			c.fail(s.Name, "command", "argümanlar satır sonu içeremez ve en fazla 4096 karakter olabilir")
			break
		}
	}
}

// splitCommand splits a command string the way compose does (POSIX shell
// word splitting with quotes and backslashes, without expansion).
func splitCommand(s string) ([]string, error) {
	var out []string
	var cur strings.Builder
	inWord := false
	var quote byte
	for i := 0; i < len(s); i++ {
		ch := s[i]
		switch {
		case quote == '\'':
			if ch == '\'' {
				quote = 0
			} else {
				cur.WriteByte(ch)
			}
		case quote == '"':
			if ch == '"' {
				quote = 0
			} else if ch == '\\' && i+1 < len(s) && strings.IndexByte("\"\\$`", s[i+1]) >= 0 {
				i++
				cur.WriteByte(s[i])
			} else {
				cur.WriteByte(ch)
			}
		case ch == '\'' || ch == '"':
			quote = ch
			inWord = true
		case ch == '\\':
			if i+1 < len(s) {
				i++
				cur.WriteByte(s[i])
				inWord = true
			}
		case ch == ' ' || ch == '\t' || ch == '\n' || ch == '\r':
			if inWord {
				out = append(out, cur.String())
				cur.Reset()
				inWord = false
			}
		default:
			cur.WriteByte(ch)
			inWord = true
		}
	}
	if quote != 0 {
		return nil, errors.New("kapanmayan tırnak işareti")
	}
	if inWord {
		out = append(out, cur.String())
	}
	return out, nil
}

func validDuration(v string) bool {
	d, err := time.ParseDuration(v)
	return err == nil && d >= time.Second && d <= time.Hour
}

func (c *converter) serviceHealth(s *ServiceSpec, n *cnode) {
	if n.kind != cMap {
		c.fail(s.Name, "healthcheck", "eşleme bekleniyor")
		return
	}
	h := &HealthSpec{}
	ok := true
	for _, k := range n.keys {
		v := n.fields[k]
		key := "healthcheck." + k
		switch k {
		case "test":
			switch v.kind {
			case cScalar:
				if t, good := c.text(v, s.Name, key); good {
					h.Test = []string{"CMD-SHELL", t}
				} else {
					ok = false
				}
			case cList:
				list, good := c.stringList(v, s.Name, key)
				if !good {
					ok = false
					continue
				}
				if len(list) > 0 && list[0] == "NONE" {
					c.fail(s.Name, key, "görüntünün sağlık denetimini kapatmak (NONE) desteklenmez")
					ok = false
					continue
				}
				if len(list) < 2 || (list[0] != "CMD" && list[0] != "CMD-SHELL") {
					c.fail(s.Name, key, `["CMD", ...] veya ["CMD-SHELL", "..."] biçiminde olmalıdır`)
					ok = false
					continue
				}
				h.Test = list
			default:
				c.fail(s.Name, key, "metin veya liste bekleniyor")
				ok = false
			}
		case "interval", "timeout", "start_period":
			t, good := c.text(v, s.Name, key)
			if !good || !validDuration(t) {
				if good {
					c.fail(s.Name, key, "%q geçersiz (1 saniye ile 1 saat arasında, örnek: 30s)", t)
				}
				ok = false
				continue
			}
			switch k {
			case "interval":
				h.Interval = t
			case "timeout":
				h.Timeout = t
			default:
				h.StartPeriod = t
			}
		case "retries":
			t, good := c.text(v, s.Name, key)
			r, err := strconv.Atoi(t)
			if !good || err != nil || r < 0 || r > 100 {
				c.fail(s.Name, key, "0 ile 100 arasında bir sayı olmalıdır")
				ok = false
				continue
			}
			h.Retries = r
		case "disable":
			if b, good := c.boolean(v, s.Name, key); good && b {
				c.fail(s.Name, key, "görüntünün sağlık denetimini kapatmak desteklenmez")
				ok = false
			}
		default:
			c.fail(s.Name, key, "%s", genericRefusal)
			ok = false
		}
	}
	if ok && len(h.Test) == 0 {
		c.fail(s.Name, "healthcheck.test", "sağlık denetimi komutu (test) zorunludur")
		return
	}
	for _, t := range h.Test {
		if !validEnvValue(t) {
			c.fail(s.Name, "healthcheck.test", "komut satır sonu içeremez")
			return
		}
	}
	if ok {
		s.Healthcheck = h
	}
}

func (c *converter) serviceDepends(s *ServiceSpec, n *cnode, names map[string]bool) {
	add := func(dep string) {
		if !names[dep] || dep == s.Name {
			c.fail(s.Name, "depends_on", "%q bilinmeyen bir servis", dep)
			return
		}
		if !contains(s.DependsOn, dep) {
			s.DependsOn = append(s.DependsOn, dep)
		}
	}
	switch n.kind {
	case cList:
		for _, it := range n.items {
			if d, ok := c.text(it, s.Name, "depends_on"); ok {
				add(d)
			}
		}
	case cMap:
		for _, dep := range n.keys {
			v := n.fields[dep]
			good := true
			if v.kind == cMap {
				for _, k := range v.keys {
					key := "depends_on." + dep + "." + k
					switch k {
					case "condition":
						cond, ok := c.text(v.fields[k], s.Name, key)
						if !ok {
							good = false
							continue
						}
						switch cond {
						case "service_started", "service_healthy":
						case "service_completed_successfully":
							c.fail(s.Name, key, "tek seferlik çalışıp biten servisler desteklenmez; panel her servisin sürekli çalışmasını bekler")
							good = false
						default:
							c.fail(s.Name, key, "%q bilinmiyor", cond)
							good = false
						}
					case "required":
						if b, ok := c.boolean(v.fields[k], s.Name, key); ok && !b {
							c.fail(s.Name, key, "isteğe bağlı bağımlılık desteklenmez")
							good = false
						}
					default:
						c.fail(s.Name, key, "%s", genericRefusal)
						good = false
					}
				}
			} else if v.kind != cNull {
				c.fail(s.Name, "depends_on."+dep, "anlaşılamadı")
				good = false
			}
			if good {
				add(dep)
			}
		}
	default:
		c.fail(s.Name, "depends_on", "liste veya eşleme bekleniyor")
	}
}

func (c *converter) serviceDevices(s *ServiceSpec, n *cnode) {
	if n.kind != cList {
		c.fail(s.Name, "devices", "liste bekleniyor")
		return
	}
	for _, it := range n.items {
		var d DeviceSpec
		switch it.kind {
		case cScalar:
			raw, ok := c.text(it, s.Name, "devices")
			if !ok {
				continue
			}
			parts := strings.Split(raw, ":")
			if len(parts) > 3 {
				c.fail(s.Name, "devices", "%q anlaşılamadı", raw)
				continue
			}
			d.Host = parts[0]
			if len(parts) > 1 {
				d.Container = parts[1]
			}
			if len(parts) > 2 {
				d.Permissions = parts[2]
			}
		case cMap:
			for _, k := range it.keys {
				t, ok := c.text(it.fields[k], s.Name, "devices."+k)
				if !ok {
					continue
				}
				switch k {
				case "source":
					d.Host = t
				case "target":
					d.Container = t
				case "permissions":
					d.Permissions = t
				default:
					c.fail(s.Name, "devices."+k, "%s", genericRefusal)
				}
			}
		default:
			c.fail(s.Name, "devices", "aygıt tanımı anlaşılamadı")
			continue
		}
		if d.Container == "" {
			d.Container = d.Host
		}
		if d.Permissions == "" {
			d.Permissions = "rwm"
		}
		if !validDevice(d.Host) || !validDevice(d.Container) {
			c.fail(s.Name, "devices", "%q geçersiz; aygıtlar /dev altında olmalıdır", d.Host)
			continue
		}
		if !validDevicePerm(d.Permissions) {
			c.fail(s.Name, "devices", "%s: izinler %q geçersiz (r, w, m)", d.Host, d.Permissions)
			continue
		}
		s.Devices = append(s.Devices, d)
	}
}

// size converts a compose size ("128m", "1gb", 268435456) to the manifest
// form ("128m").
func (c *converter) size(n *cnode, service, key string) (string, bool) {
	t, ok := c.text(n, service, key)
	if !ok {
		return "", false
	}
	if sz, ok := composeSize(t); ok {
		return sz, true
	}
	c.fail(service, key, "%q geçersiz boyut (örnek: 128m, 1g)", t)
	return "", false
}

func composeSize(t string) (string, bool) {
	t = strings.ToLower(strings.TrimSpace(t))
	if _, err := ParseSize(t); err == nil {
		return t, true
	}
	if strings.HasSuffix(t, "b") {
		t = strings.TrimSuffix(t, "b")
	}
	n, err := strconv.ParseInt(t, 10, 64)
	if err != nil || n <= 0 {
		return "", false
	}
	for _, u := range []struct {
		suffix string
		div    int64
	}{{"g", 1 << 30}, {"m", 1 << 20}, {"k", 1 << 10}} {
		if n%u.div == 0 {
			out := strconv.FormatInt(n/u.div, 10) + u.suffix
			if _, err := ParseSize(out); err == nil {
				return out, true
			}
		}
	}
	return "", false
}

func (c *converter) serviceTmpfs(s *ServiceSpec, n *cnode) {
	var list []string
	switch n.kind {
	case cScalar:
		if t, ok := c.text(n, s.Name, "tmpfs"); ok {
			list = []string{t}
		}
	case cList:
		list, _ = c.stringList(n, s.Name, "tmpfs")
	default:
		c.fail(s.Name, "tmpfs", "metin veya liste bekleniyor")
		return
	}
	for _, raw := range list {
		target, opts, _ := strings.Cut(raw, ":")
		t := TmpfsSpec{Target: target}
		good := true
		if opts != "" {
			for _, o := range strings.Split(opts, ",") {
				k, v, _ := strings.Cut(o, "=")
				if k != "size" {
					c.fail(s.Name, "tmpfs", "%q: %q seçeneği desteklenmez (yalnızca size)", raw, k)
					good = false
					continue
				}
				sz, ok := composeSize(v)
				if !ok {
					c.fail(s.Name, "tmpfs", "%q: boyut geçersiz", raw)
					good = false
					continue
				}
				t.Size = sz
			}
		}
		if !cleanAbs(target) || target == "/" {
			c.fail(s.Name, "tmpfs", "%q geçerli bir konteyner yolu değil", target)
			continue
		}
		if good {
			s.Tmpfs = append(s.Tmpfs, t)
		}
	}
}

/* ---------- naming and canonical form ---------- */

var slugTR = strings.NewReplacer("ç", "c", "Ç", "c", "ğ", "g", "Ğ", "g", "ı", "i", "İ", "i",
	"ö", "o", "Ö", "o", "ş", "s", "Ş", "s", "ü", "u", "Ü", "u")

// customSlug derives the slug of a custom application from its name:
// "Uptime Kuma" becomes "custom-uptime-kuma".
func customSlug(name string) string {
	s := strings.ToLower(slugTR.Replace(name))
	var b strings.Builder
	dash := false
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			dash = false
		} else if b.Len() > 0 && !dash {
			b.WriteByte('-')
			dash = true
		}
	}
	out := strings.Trim(b.String(), "-")
	if max := 32 - len(CustomPrefix); len(out) > max {
		out = strings.TrimRight(out[:max], "-")
	}
	if out == "" {
		return ""
	}
	return CustomPrefix + out
}

// manifestYAML renders a manifest as YAML without empty fields. Parse reads
// it back into the same manifest.
func manifestYAML(m *Manifest) ([]byte, error) {
	var n yaml.Node
	if err := n.Encode(m); err != nil {
		return nil, err
	}
	pruneEmpty(&n)
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(&n); err != nil {
		return nil, err
	}
	if err := enc.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func pruneEmpty(n *yaml.Node) {
	for _, c := range n.Content {
		pruneEmpty(c)
	}
	if n.Kind != yaml.MappingNode {
		return
	}
	kept := make([]*yaml.Node, 0, len(n.Content))
	for i := 0; i+1 < len(n.Content); i += 2 {
		if emptyYAML(n.Content[i+1]) {
			continue
		}
		kept = append(kept, n.Content[i], n.Content[i+1])
	}
	n.Content = kept
}

func emptyYAML(v *yaml.Node) bool {
	switch v.Kind {
	case yaml.ScalarNode:
		switch v.ShortTag() {
		case "!!null":
			return true
		case "!!str":
			return v.Value == ""
		case "!!bool":
			return v.Value == "false"
		case "!!int":
			return v.Value == "0"
		}
	case yaml.SequenceNode, yaml.MappingNode:
		return len(v.Content) == 0
	}
	return false
}
