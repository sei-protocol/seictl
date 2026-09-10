package cliutil

import (
	"strings"
	"testing"
)

// The supported set is deliberately one named offering plus the standard
// tier. These pin what `sei-gp3-performance-v1` encodes, so a catalog edit
// that changed the pair beside an unchanged -v1 name — the exact mistake
// immutable VAC parameters make unrecoverable on a live cluster — fails
// here instead of at provision time.
func TestStoragePerformanceCatalog_Pins(t *testing.T) {
	if len(storagePerformanceCatalog) != 1 {
		t.Fatalf("catalog holds %d offerings; want exactly 1 (the platform ships one class). "+
			"Adding one is a platform/GitOps change first — update this test and the tier-minimum table with it",
			len(storagePerformanceCatalog))
	}
	got := storagePerformanceCatalog[0]
	want := StoragePerformanceOffering{IOPS: 10000, Throughput: 750, ClassName: "sei-gp3-performance-v1"}
	if got != want {
		t.Errorf("catalog[0] = %+v; want %+v", got, want)
	}
	if !strings.HasSuffix(got.ClassName, "-v1") {
		t.Errorf("class name %q lost its generation suffix: VAC parameters are immutable, "+
			"so a retune is a new object (-v2), never an edit of this one", got.ClassName)
	}
}

// One case per tier, pinning the smallest data volume its IOPS is legal
// on. A future retune that raises IOPS raises this floor, and a floor
// that climbs past a documented preset default has silently invalidated
// it — see TestRender_PerformanceTierFitsPresetDefault in seinode and
// seinetwork, which hold the other half of that pairing.
func TestStoragePerformanceOffering_MinSizeGiBPerTier(t *testing.T) {
	want := map[string]int64{
		"sei-gp3-performance-v1": 20, // 10000 IOPS / 500 IOPS-per-GiB
	}
	for _, o := range storagePerformanceCatalog {
		t.Run(o.ClassName, func(t *testing.T) {
			expected, ok := want[o.ClassName]
			if !ok {
				t.Fatalf("offering %q has no pinned minimum size: add one here when you add the offering", o.ClassName)
			}
			if got := o.MinSizeGiB(); got != expected {
				t.Errorf("MinSizeGiB() = %d; want %d (%d IOPS / %d IOPS-per-GiB)",
					got, expected, o.IOPS, gp3MaxIOPSPerGiB)
			}
		})
	}
	if len(want) != len(storagePerformanceCatalog) {
		t.Errorf("pinned %d minimums for %d offerings: every tier needs one", len(want), len(storagePerformanceCatalog))
	}
}

// Rounding up matters: 10001 IOPS on 20 GiB is 500.05 IOPS/GiB and would
// be rejected by EBS, so the floor must be the next whole GiB, not the
// truncated one.
func TestStoragePerformanceOffering_MinSizeGiBRoundsUp(t *testing.T) {
	cases := []struct {
		iops int64
		want int64
	}{
		{iops: 500, want: 1},
		{iops: 501, want: 2},
		{iops: 10000, want: 20},
		{iops: 10001, want: 21},
	}
	for _, tc := range cases {
		o := StoragePerformanceOffering{IOPS: tc.iops}
		if got := o.MinSizeGiB(); got != tc.want {
			t.Errorf("MinSizeGiB() for %d IOPS = %d; want %d", tc.iops, got, tc.want)
		}
	}
}

func TestResolveStoragePerformance_StandardTierIsBothUnset(t *testing.T) {
	name, err := ResolveStoragePerformance("", "")
	if err != nil {
		t.Fatalf("ResolveStoragePerformance(\"\", \"\") = %v; want nil: omitting both flags is a legal selection", err)
	}
	if name != "" {
		t.Errorf("class name = %q; want empty so the field is left off the CR entirely", name)
	}
}

func TestResolveStoragePerformance_SupportedPairResolvesToItsClass(t *testing.T) {
	name, err := ResolveStoragePerformance("10000", "750")
	if err != nil {
		t.Fatalf("ResolveStoragePerformance(10000, 750) = %v; want the performance class", err)
	}
	if name != "sei-gp3-performance-v1" {
		t.Errorf("class name = %q; want sei-gp3-performance-v1", name)
	}
}

// Req 3.4 + 3.5: refuse, and name the supported set with each pair
// alongside the class it resolves to.
func TestResolveStoragePerformance_RejectsUnsupportedPair(t *testing.T) {
	cases := []struct{ iops, throughput string }{
		{"5000", "500"},   // neither half supported
		{"10000", "500"},  // right IOPS, wrong throughput
		{"5000", "750"},   // wrong IOPS, right throughput
		{"16000", "1000"}, // a real gp3 maximum, but no class encodes it
	}
	for _, tc := range cases {
		t.Run(tc.iops+"/"+tc.throughput, func(t *testing.T) {
			name, err := ResolveStoragePerformance(tc.iops, tc.throughput)
			if err == nil {
				t.Fatalf("ResolveStoragePerformance(%s, %s) = %q, nil; want a refusal", tc.iops, tc.throughput, name)
			}
			assertNamesSupportedSet(t, err.Error())
		})
	}
}

// Half a pair is not a selection: honoring it would mean inventing the
// other half.
func TestResolveStoragePerformance_RejectsHalfAPair(t *testing.T) {
	cases := []struct{ name, iops, throughput string }{
		{"iops alone", "10000", ""},
		{"throughput alone", "", "750"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ResolveStoragePerformance(tc.iops, tc.throughput)
			if err == nil {
				t.Fatal("ResolveStoragePerformance = nil; want a refusal")
			}
			if !strings.Contains(err.Error(), "as a pair") {
				t.Errorf("err = %q; want it to say the flags are a pair", err.Error())
			}
			assertNamesSupportedSet(t, err.Error())
		})
	}
}

func TestResolveStoragePerformance_RejectsNonInteger(t *testing.T) {
	cases := []struct{ name, iops, throughput string }{
		{"iops with a unit suffix", "10k", "750"},
		{"throughput as a quantity", "10000", "750Mi"},
		{"iops with a decimal point", "10000.0", "750"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ResolveStoragePerformance(tc.iops, tc.throughput)
			if err == nil {
				t.Fatal("ResolveStoragePerformance = nil; want a refusal")
			}
			if !strings.Contains(err.Error(), "is not a plain integer") {
				t.Errorf("err = %q; want it rejected as unparseable, not as an unsupported pair", err.Error())
			}
			assertNamesSupportedSet(t, err.Error())
		})
	}
}

// Zero and negative parse cleanly, so only the sign check gives them
// their own message. Without it they fall through to the catalog miss and
// are reported as an unsupported pair — true, but it buries a typo in a
// menu of offerings that were never the problem.
func TestResolveStoragePerformance_RejectsNonPositive(t *testing.T) {
	cases := []struct{ name, iops, throughput string }{
		{"iops zero", "0", "750"},
		{"throughput negative", "10000", "-750"},
		{"both zero", "0", "0"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ResolveStoragePerformance(tc.iops, tc.throughput)
			if err == nil {
				t.Fatal("ResolveStoragePerformance = nil; want a refusal")
			}
			if !strings.Contains(err.Error(), "must be positive") {
				t.Errorf("err = %q; want it rejected for not being positive, not as an unsupported pair", err.Error())
			}
			assertNamesSupportedSet(t, err.Error())
		})
	}
}

// assertNamesSupportedSet holds Req 3.5: a refusal is only actionable if
// it carries the whole menu — each pair, the class it resolves to, and
// the standard tier that needs no flags at all.
func assertNamesSupportedSet(t *testing.T, msg string) {
	t.Helper()
	for _, want := range []string{"10000", "750", "sei-gp3-performance-v1", "omit both flags"} {
		if !strings.Contains(msg, want) {
			t.Errorf("refusal = %q; want it to name %q from the supported set", msg, want)
		}
	}
}

// storageObject builds the rendered shape the guard reads: the size and
// the class name as they sit on spec.dataVolume.storage.
func storageObject(className string, size interface{}) map[string]interface{} {
	storage := map[string]interface{}{}
	if className != "" {
		storage["volumeAttributesClassName"] = className
	}
	if size != nil {
		storage["resources"] = map[string]interface{}{
			"requests": map[string]interface{}{"storage": size},
		}
	}
	return map[string]interface{}{
		"spec": map[string]interface{}{
			"dataVolume": map[string]interface{}{"storage": storage},
		},
	}
}

func TestValidateStoragePerformanceSelection_Accepts(t *testing.T) {
	cases := []struct {
		name string
		root map[string]interface{}
	}{
		{"empty object", map[string]interface{}{}},
		// The standard tier: no class name at all. DR-001:81-88 makes the
		// unset field a legal selection, not a missing one.
		{"no class name", storageObject("", "500Gi")},
		{"no class name and no size", storageObject("", nil)},
		{"performance on the preset default", storageObject("sei-gp3-performance-v1", "500Gi")},
		{"performance exactly at the floor", storageObject("sei-gp3-performance-v1", "20Gi")},
		// No size on the object: the controller's per-mode default applies
		// and the harness cannot see it, so there is no ratio to check.
		{"performance with no size", storageObject("sei-gp3-performance-v1", nil)},
		// A --set of an all-digit value lands as an int64 byte count.
		{"size as a bare int64 byte count", storageObject("sei-gp3-performance-v1", int64(21474836480))},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := ValidateStoragePerformanceSelection(tc.root); err != nil {
				t.Errorf("ValidateStoragePerformanceSelection = %v; want nil", err)
			}
		})
	}
}

// The --set smuggling guard, mirroring RejectCPULimit: no discrete flag
// writes a class name, but --set reaches the path directly.
func TestValidateStoragePerformanceSelection_RejectsUnknownClassName(t *testing.T) {
	cases := []string{
		"sei-gp3-performance-v2", // a plausible future retune that does not exist yet
		"sei-gp3-performance",    // the suffix stripped — a different object
		"sei-gp3-archive-v1",     // a tier that was deliberately never minted
		"gp3",                    // a StorageClass name, not a VAC
	}
	for _, name := range cases {
		t.Run(name, func(t *testing.T) {
			err := ValidateStoragePerformanceSelection(storageObject(name, "500Gi"))
			if err == nil {
				t.Fatalf("ValidateStoragePerformanceSelection(%q) = nil; want a refusal", name)
			}
			if !strings.Contains(err.Error(), name) {
				t.Errorf("err = %q; want it to name the rejected class %q", err.Error(), name)
			}
			assertNamesSupportedSet(t, err.Error())
		})
	}
}

// The ratio ceiling. CEL cannot see this — the size and the class name are
// independent fields and 500 IOPS/GiB is an AWS provisioning rule — so
// admission accepts the pair and the PVC fails to provision.
func TestValidateStoragePerformanceSelection_RejectsVolumeTooSmallForIOPS(t *testing.T) {
	cases := []struct {
		name string
		size interface{}
	}{
		{"the task's 10Gi case", "10Gi"},
		{"one GiB under the floor", "19Gi"},
		{"a decimal spelling under the floor", "20G"}, // 20 GB < 20 GiB
		{"a bare int64 byte count under the floor", int64(10737418240)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateStoragePerformanceSelection(storageObject("sei-gp3-performance-v1", tc.size))
			if err == nil {
				t.Fatalf("ValidateStoragePerformanceSelection(size=%v) = nil; want a refusal", tc.size)
			}
			msg := err.Error()
			// The remedy is the whole point: name the floor and say the
			// field is create-only, or the operator edits and waits.
			for _, want := range []string{"20Gi", "sei-gp3-performance-v1", "create-only"} {
				if !strings.Contains(msg, want) {
					t.Errorf("err = %q; want it to name %q", msg, want)
				}
			}
		})
	}
}

func TestValidateStoragePerformanceSelection_RejectsUnparseableSize(t *testing.T) {
	err := ValidateStoragePerformanceSelection(storageObject("sei-gp3-performance-v1", "500 Gi"))
	if err == nil {
		t.Fatal("ValidateStoragePerformanceSelection = nil; want a refusal")
	}
	if !strings.Contains(err.Error(), "spec.dataVolume.storage.resources.requests.storage") {
		t.Errorf("err = %q; want it to name the offending path", err.Error())
	}
}
