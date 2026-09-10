package cliutil

import (
	"fmt"
	"strconv"
	"strings"

	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// StoragePerformanceOffering is one supported point on the gp3
// performance menu: a pair of the standard performance parameters, and
// the VolumeAttributesClass that encodes that pair.
//
// The direction is one-way (Spec 001 Req 3.2): the operator supplies the
// pair, the harness resolves the name. Nothing in this file accepts a
// class name as operator input — ValidateStoragePerformanceSelection
// reads one back off the rendered object only to reject a name that no
// offering resolves to.
type StoragePerformanceOffering struct {
	IOPS       int64  // provisioned IOPS
	Throughput int64  // MiB/s
	ClassName  string // the VolumeAttributesClass that encodes the pair
}

// storagePerformanceCatalog mirrors the VolumeAttributesClass objects the
// platform ships by GitOps (platform#1661,
// clusters/base/default/volume-attributes-class.yaml). The platform owns
// the catalog, the harness owns the menu (DR-001:101-106): an entry here
// is only correct once the matching class is on the cluster, and adding a
// new (IOPS, throughput) point is a platform change first.
//
// The -v1 suffix is load-bearing. VolumeAttributesClass `parameters` are
// IMMUTABLE, so retuning a tier means a NEW object (sei-gp3-performance-v2)
// and a new entry here — never an edit of the pair beside an unchanged
// name. Editing the pair in place would silently repoint every future
// render at a class whose parameters no longer match.
var storagePerformanceCatalog = []StoragePerformanceOffering{
	{IOPS: 10000, Throughput: 750, ClassName: "sei-gp3-performance-v1"},
}

// gp3MaxIOPSPerGiB is the EBS gp3 ratio ceiling: provisioned IOPS may not
// exceed 500 x volume size in GiB. The pairing is checked at PROVISION
// time by the EBS CSI driver, not at admission — CEL cannot see it,
// because the size and the class name are two independent fields and the
// ratio is an AWS rule, not a schema rule.
//
// So an illegal pairing costs a full round trip: the CR is accepted, the
// PVC is created, and the pod sits Pending on a ProvisioningFailed volume.
// Both fields are create-only on the CRD, so the remedy is a NEW chain,
// not an edit. That is the whole reason this is enforced locally.
const gp3MaxIOPSPerGiB = 500

// MinSizeGiB is the smallest data volume this offering's IOPS is legal on,
// rounding up: 10000 IOPS needs 20 GiB.
func (o StoragePerformanceOffering) MinSizeGiB() int64 {
	return (o.IOPS + gp3MaxIOPSPerGiB - 1) / gp3MaxIOPSPerGiB
}

// gibiByte is the unit EBS provisions in. Volumes have whole-GiB capacity;
// there is no such thing as a 19.5 GiB volume.
const gibiByte = 1 << 30

// effectiveSizeGiB is the volume the EBS CSI driver actually creates for a
// requested capacity. The driver rounds the request UP to a whole GiB
// (RoundUpGiB in the driver's pkg/util, called from CreateVolume), so a
// request is never provisioned smaller than it asked for.
//
// The ratio ceiling applies to that PROVISIONED size, not to the raw
// request. Comparing the request directly would refuse a selection AWS
// accepts: a request of 19.5Gi provisions a 20 GiB volume, which carries
// 10000 IOPS at exactly 500 IOPS per GiB. A local validator that predicts
// a provisioning failure which would not happen is worse than no validator
// — it sends the operator to resize a chain that was already legal.
func effectiveSizeGiB(q resource.Quantity) int64 {
	// Value() already rounds a fractional quantity up to whole bytes.
	bytes := q.Value()
	if bytes <= 0 {
		return 0
	}
	return (bytes + gibiByte - 1) / gibiByte
}

// StoragePerformanceMenu renders the supported set for an error message:
// every offering's pair alongside the class name it resolves to, plus the
// standard tier (Spec 001 Req 3.5).
//
// The standard tier is a legal selection, not the absence of one
// (DR-001:81-88): omitting both flags leaves volumeAttributesClassName
// unset on the PVC, and the gp3 StorageClass defaults supply the baseline.
// A menu that listed only the named classes would read as if a selection
// were mandatory.
func StoragePerformanceMenu() string {
	var b strings.Builder
	b.WriteString("supported storage performance offerings:\n")
	for _, o := range storagePerformanceCatalog {
		fmt.Fprintf(&b, "  --iops %d --throughput %d  ->  %s (needs --storage >= %dGi)\n",
			o.IOPS, o.Throughput, o.ClassName, o.MinSizeGiB())
	}
	b.WriteString("  (omit both flags)         ->  standard: no volumeAttributesClassName, the gp3 StorageClass defaults apply")
	return b.String()
}

// StoragePerformanceOfferings returns the supported set. Callers that
// need to exercise every tier — rather than the one that happens to be
// shipping today — range over this, so a tier added here is covered
// without an edit at the call site.
func StoragePerformanceOfferings() []StoragePerformanceOffering {
	out := make([]StoragePerformanceOffering, len(storagePerformanceCatalog))
	copy(out, storagePerformanceCatalog)
	return out
}

// offeringByClassName finds the offering a class name encodes.
func offeringByClassName(name string) (StoragePerformanceOffering, bool) {
	for _, o := range storagePerformanceCatalog {
		if o.ClassName == name {
			return o, true
		}
	}
	return StoragePerformanceOffering{}, false
}

// ResolveStoragePerformance maps an operator-supplied (IOPS, throughput)
// pair to the name of the VolumeAttributesClass that encodes it. An empty
// string means the standard tier: no class name, so the caller leaves
// spec.dataVolume.storage.volumeAttributesClassName unset.
//
// Refusing here rather than at admission is the point (Spec 001 Req 3.4).
// In the GitOps flow an unsupported pair that renders cleanly is not
// discovered until the CR has been committed, merged, and reconciled by
// Flux — and because the field is create-only, the remedy at that stage is
// a new chain rather than an edit.
func ResolveStoragePerformance(iops, throughput string) (string, error) {
	if iops == "" && throughput == "" {
		return "", nil
	}
	if iops == "" || throughput == "" {
		// A pair is one selection. Honoring a lone --iops would mean
		// inventing the throughput half of a pair the operator never chose.
		return "", UsageError(
			"--iops and --throughput select a storage performance offering as a pair: pass both, or neither for the standard tier\n%s",
			StoragePerformanceMenu())
	}
	iopsVal, err := parsePerformanceParam("iops", iops)
	if err != nil {
		return "", err
	}
	throughputVal, err := parsePerformanceParam("throughput", throughput)
	if err != nil {
		return "", err
	}
	for _, o := range storagePerformanceCatalog {
		if o.IOPS == iopsVal && o.Throughput == throughputVal {
			return o.ClassName, nil
		}
	}
	return "", UsageError(
		"--iops %d --throughput %d is not a supported storage performance offering: the platform ships one VolumeAttributesClass per supported pair, and no class encodes this one\n%s",
		iopsVal, throughputVal, StoragePerformanceMenu())
}

// parsePerformanceParam reads one half of the pair as a plain positive
// integer. The units are fixed by the offering (IOPS, and MiB/s for
// throughput), so a Kubernetes quantity spelling like "750Mi" is a
// different unit system and is rejected rather than guessed at.
func parsePerformanceParam(flag, value string) (int64, error) {
	n, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return 0, UsageError(
			"--%s %q is not a plain integer: pass IOPS as a count and throughput as MiB/s (e.g. --iops 10000 --throughput 750)\n%s",
			flag, value, StoragePerformanceMenu())
	}
	if n <= 0 {
		return 0, UsageError("--%s %q must be positive\n%s", flag, value, StoragePerformanceMenu())
	}
	return n, nil
}

// ValidateStoragePerformanceSelection is the final guard on the rendered
// object, mirroring RejectCPULimit: it re-reads what actually landed on
// spec.dataVolume.storage after every layer, so --set cannot smuggle in a
// class name that no supported pair resolves to, and cannot pair a
// supported class with a data volume too small to carry its IOPS.
//
// Reading the object rather than the flags is what makes the guard total:
// --set spec.dataVolume.storage.volumeAttributesClassName=... and
// --set spec.dataVolume.storage.resources.requests.storage=... both reach
// these paths without going through ResolveStoragePerformance.
func ValidateStoragePerformanceSelection(root map[string]interface{}) error {
	name, found, err := unstructured.NestedString(root, "spec", "dataVolume", "storage", "volumeAttributesClassName")
	if err != nil {
		return UsageError("read spec.dataVolume.storage.volumeAttributesClassName: %s", err.Error())
	}
	if !found {
		// The standard tier — a legal selection (DR-001:81-88), not a gap.
		return nil
	}
	offering, ok := offeringByClassName(name)
	if !ok {
		return UsageError(
			"spec.dataVolume.storage.volumeAttributesClassName %q is not a supported storage performance offering: select one by its (IOPS, throughput) pair rather than by name, so the pair and the class cannot drift apart\n%s",
			name, StoragePerformanceMenu())
	}
	return validateSizeCarriesIOPS(root, offering)
}

// validateSizeCarriesIOPS enforces the gp3 ratio ceiling against the size
// that actually landed on the object — the preset default when --storage
// was not passed.
func validateSizeCarriesIOPS(root map[string]interface{}, offering StoragePerformanceOffering) error {
	raw, found, err := unstructured.NestedFieldNoCopy(root, "spec", "dataVolume", "storage", "resources", "requests", "storage")
	if err != nil {
		return UsageError("read spec.dataVolume.storage.resources.requests.storage: %s", err.Error())
	}
	if !found {
		// No size on the object: the controller's per-mode default applies
		// and the harness cannot see it, so there is no ratio to check here.
		// Refusing would be a guess, not a catch.
		return nil
	}
	size, err := quantityFromUnstructured(raw)
	if err != nil {
		return UsageError("spec.dataVolume.storage.resources.requests.storage: %s", err.Error())
	}
	provisioned := effectiveSizeGiB(size)
	if provisioned < offering.MinSizeGiB() {
		return UsageError(
			"data volume %s provisions %d GiB, too small for %s (%d IOPS / %d MiB/s): EBS gp3 caps IOPS at %d x volume size in GiB, so this offering needs at least %dGi. "+
				"The apiserver accepts this pair — the ratio is an AWS provisioning rule, not a schema rule — and the PVC then fails to provision, leaving the pod Pending on ProvisioningFailed. "+
				"Both fields are create-only on the CRD, so the remedy at that point is a new chain: raise --storage to %dGi or more, or omit --iops/--throughput for the standard tier",
			size.String(), provisioned, offering.ClassName, offering.IOPS, offering.Throughput, gp3MaxIOPSPerGiB, offering.MinSizeGiB(), offering.MinSizeGiB())
	}
	return nil
}

// quantityFromUnstructured reads an int-or-string Quantity field. A --set
// value arrives as an int64 when it is all digits (parse.go parseValue)
// and as a string otherwise, and preset YAML always decodes to a string.
func quantityFromUnstructured(raw interface{}) (resource.Quantity, error) {
	switch v := raw.(type) {
	case string:
		q, err := resource.ParseQuantity(v)
		if err != nil {
			return resource.Quantity{}, fmt.Errorf("%q is not a valid Kubernetes quantity: %s", v, err.Error())
		}
		return q, nil
	case int64:
		return *resource.NewQuantity(v, resource.DecimalSI), nil
	case float64:
		// JSON numbers decode to float64 through some paths; a byte count
		// is an integer, so truncation here only ever drops a fraction.
		return *resource.NewQuantity(int64(v), resource.DecimalSI), nil
	default:
		return resource.Quantity{}, fmt.Errorf("expected a quantity, got %T", raw)
	}
}
