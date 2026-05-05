package nodedeployment

import (
	"bytes"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func sampleSND() *unstructured.Unstructured {
	obj := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "sei.io/v1alpha1",
			"kind":       "SeiNodeDeployment",
			"metadata":   map[string]interface{}{"name": "demo", "namespace": "nightly"},
			"spec":       map[string]interface{}{"replicas": int64(2)},
		},
	}
	return obj
}

func TestMakePrinter_Formats(t *testing.T) {
	cases := []struct {
		format   string
		contains []string
	}{
		{"", []string{"apiVersion: sei.io/v1alpha1", "kind: SeiNodeDeployment"}},
		{"yaml", []string{"apiVersion: sei.io/v1alpha1", "kind: SeiNodeDeployment"}},
		{"json", []string{`"apiVersion": "sei.io/v1alpha1"`, `"kind": "SeiNodeDeployment"`}},
		{"name", []string{"seinodedeployment.sei.io/demo"}},
		{"jsonpath={.metadata.name}", []string{"demo"}},
	}
	for _, tc := range cases {
		t.Run(tc.format, func(t *testing.T) {
			p, err := makePrinter(tc.format)
			if err != nil {
				t.Fatalf("makePrinter(%q): %v", tc.format, err)
			}
			var buf bytes.Buffer
			if err := p.PrintObj(sampleSND(), &buf); err != nil {
				t.Fatalf("PrintObj: %v", err)
			}
			for _, want := range tc.contains {
				if !strings.Contains(buf.String(), want) {
					t.Errorf("output missing %q\ngot:\n%s", want, buf.String())
				}
			}
		})
	}
}

func TestMakePrinter_Errors(t *testing.T) {
	cases := []string{"xml", "table", "jsonpath=", "jsonpath={.bad"}
	for _, format := range cases {
		t.Run(format, func(t *testing.T) {
			if _, err := makePrinter(format); err == nil {
				t.Errorf("expected error for format %q", format)
			}
		})
	}
}

func TestMakePrinter_NamePrintsListItems(t *testing.T) {
	list := &unstructured.UnstructuredList{
		Object: map[string]interface{}{
			"apiVersion": "sei.io/v1alpha1",
			"kind":       "SeiNodeDeploymentList",
		},
		Items: []unstructured.Unstructured{
			{Object: map[string]interface{}{
				"apiVersion": "sei.io/v1alpha1",
				"kind":       "SeiNodeDeployment",
				"metadata":   map[string]interface{}{"name": "a", "namespace": "ns1"},
			}},
			{Object: map[string]interface{}{
				"apiVersion": "sei.io/v1alpha1",
				"kind":       "SeiNodeDeployment",
				"metadata":   map[string]interface{}{"name": "b", "namespace": "ns1"},
			}},
		},
	}
	p, err := makePrinter("name")
	if err != nil {
		t.Fatalf("makePrinter: %v", err)
	}
	var buf bytes.Buffer
	if err := p.PrintObj(list, &buf); err != nil {
		t.Fatalf("PrintObj: %v", err)
	}
	out := buf.String()
	for _, want := range []string{"seinodedeployment.sei.io/a", "seinodedeployment.sei.io/b"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q\ngot:\n%s", want, out)
		}
	}
}
