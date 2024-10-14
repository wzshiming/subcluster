package sublet

import (
	"fmt"
	"io"
	"net/http"
	"net/url"

	"github.com/emicklei/go-restful/v3"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/conversion"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apiserver/pkg/util/flushwriter"
)

// getContainerLogs handles containerLogs request against the Kubelet
func (s *Server) getContainerLogs(request *restful.Request, response *restful.Response) {
	podNamespace := request.PathParameter("podNamespace")
	podName := request.PathParameter("podID")
	containerName := request.PathParameter("containerName")

	if len(podName) == 0 {
		_ = response.WriteError(http.StatusBadRequest, fmt.Errorf(`{"message": "Missing podID."}`))
		return
	}
	if len(containerName) == 0 {
		_ = response.WriteError(http.StatusBadRequest, fmt.Errorf(`{"message": "Missing restfulCont name."}`))
		return
	}
	if len(podNamespace) == 0 {
		_ = response.WriteError(http.StatusBadRequest, fmt.Errorf(`{"message": "Missing podNamespace."}`))
		return
	}

	ctx := request.Request.Context()

	query := request.Request.URL.Query()
	// backwards compatibility for the "tail" query parameter
	if tail := request.QueryParameter("tail"); len(tail) > 0 {
		query["tailLines"] = []string{tail}
		// "all" is the same as omitting tail
		if tail == "all" {
			delete(query, "tailLines")
		}
	}

	// restfulCont logs on the kubelet are locked to the corev1 API version of PodLogOptions
	logOptions := &corev1.PodLogOptions{}
	err := convert_url_Values_To_v1_PodLogOptions(&query, logOptions, nil)
	if err != nil {
		_ = response.WriteError(http.StatusBadRequest, err)
		return
	}

	if logOptions.Container == "" {
		logOptions.Container = containerName
	}

	req := s.sourceClient.CoreV1().
		Pods(s.sourceNamespace).
		GetLogs(nameToSource(s.subclusterName, podNamespace, podName), logOptions)

	stm, err := req.Stream(ctx)
	if err != nil {
		_ = response.WriteError(http.StatusInternalServerError, err)
		return
	}
	defer stm.Close()

	response.Header().Set("Transfer-Encoding", "chunked")
	fw := flushwriter.Wrap(response.ResponseWriter)
	_, err = io.Copy(fw, stm)
	if err != nil {
		if ctx.Err() == nil {
			_ = response.WriteError(http.StatusBadRequest, err)
			return
		}
	}
}

func convert_url_Values_To_v1_PodLogOptions(in *url.Values, out *corev1.PodLogOptions, s conversion.Scope) error {
	if values, ok := (*in)["container"]; ok && len(values) > 0 {
		if err := runtime.Convert_Slice_string_To_string(&values, &out.Container, s); err != nil {
			return err
		}
	} else {
		out.Container = ""
	}
	if values, ok := (*in)["follow"]; ok && len(values) > 0 {
		if err := runtime.Convert_Slice_string_To_bool(&values, &out.Follow, s); err != nil {
			return err
		}
	} else {
		out.Follow = false
	}
	if values, ok := (*in)["previous"]; ok && len(values) > 0 {
		if err := runtime.Convert_Slice_string_To_bool(&values, &out.Previous, s); err != nil {
			return err
		}
	} else {
		out.Previous = false
	}
	if values, ok := (*in)["sinceSeconds"]; ok && len(values) > 0 {
		if err := runtime.Convert_Slice_string_To_Pointer_int64(&values, &out.SinceSeconds, s); err != nil {
			return err
		}
	} else {
		out.SinceSeconds = nil
	}
	if values, ok := (*in)["sinceTime"]; ok && len(values) > 0 {
		if err := metav1.Convert_Slice_string_To_Pointer_v1_Time(&values, &out.SinceTime, s); err != nil {
			return err
		}
	} else {
		out.SinceTime = nil
	}
	if values, ok := (*in)["timestamps"]; ok && len(values) > 0 {
		if err := runtime.Convert_Slice_string_To_bool(&values, &out.Timestamps, s); err != nil {
			return err
		}
	} else {
		out.Timestamps = false
	}
	if values, ok := (*in)["tailLines"]; ok && len(values) > 0 {
		if err := runtime.Convert_Slice_string_To_Pointer_int64(&values, &out.TailLines, s); err != nil {
			return err
		}
	} else {
		out.TailLines = nil
	}
	if values, ok := (*in)["limitBytes"]; ok && len(values) > 0 {
		if err := runtime.Convert_Slice_string_To_Pointer_int64(&values, &out.LimitBytes, s); err != nil {
			return err
		}
	} else {
		out.LimitBytes = nil
	}
	if values, ok := (*in)["insecureSkipTLSVerifyBackend"]; ok && len(values) > 0 {
		if err := runtime.Convert_Slice_string_To_bool(&values, &out.InsecureSkipTLSVerifyBackend, s); err != nil {
			return err
		}
	} else {
		out.InsecureSkipTLSVerifyBackend = false
	}
	return nil
}
