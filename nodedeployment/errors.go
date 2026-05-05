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

// emitStatus writes err as a metav1.Status so callers can `jq -r
// .reason` to discriminate failure classes. Wraps non-Status errors
// as InternalError.
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

// usageError reports CLI validation failures as Status{Reason:Invalid}
// so the stderr discriminator above still works.
func usageError(format string, args ...interface{}) error {
	return apierrors.NewBadRequest(fmt.Sprintf(format, args...))
}
