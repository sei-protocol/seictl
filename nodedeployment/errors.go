package nodedeployment

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// emitStatus writes a metav1.Status object to w (typically stderr) so
// orchestrator scripts can `jq -r .reason` to discriminate timeout vs
// validation vs transient API failure without parsing exit codes. err
// is wrapped to preserve the upstream Status when available, otherwise
// shaped into a generic InternalError.
func emitStatus(w io.Writer, err error) {
	if err == nil {
		return
	}
	status := toStatus(err)
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(status)
}

func toStatus(err error) *metav1.Status {
	var apiErr apierrors.APIStatus
	if errors.As(err, &apiErr) {
		s := apiErr.Status()
		return &s
	}
	return &metav1.Status{
		TypeMeta: metav1.TypeMeta{Kind: "Status", APIVersion: "v1"},
		Status:   metav1.StatusFailure,
		Reason:   metav1.StatusReasonInternalError,
		Message:  err.Error(),
		Code:     http.StatusInternalServerError,
	}
}

// usageError signals a CLI-level validation failure (bad flags, unknown
// preset). Reported as a metav1.Status with reason=Invalid so the same
// stderr discriminator works.
func usageError(format string, args ...interface{}) error {
	return apierrors.NewBadRequest(fmt.Sprintf(format, args...))
}
