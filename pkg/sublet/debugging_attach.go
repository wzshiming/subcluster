package sublet

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/emicklei/go-restful/v3"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	remotecommandconsts "k8s.io/apimachinery/pkg/util/remotecommand"
	"k8s.io/client-go/kubernetes/scheme"
	remotecommandclient "k8s.io/client-go/tools/remotecommand"
	remotecommandserver "k8s.io/kubelet/pkg/cri/streaming/remotecommand"
)

// AttachContainer attaches to a container in a pod,
// copying data between in/out/err and the container's stdin/stdout/stderr.
func (s *Server) AttachContainer(ctx context.Context, name string, uid types.UID, container string, in io.Reader, out, errOut io.WriteCloser, tty bool, resize <-chan remotecommandclient.TerminalSize) error {
	pod := strings.Split(name, "/")
	if len(pod) != 2 {
		return fmt.Errorf("invalid pod name %q", name)
	}
	podName, podNamespace := pod[0], pod[1]

	req := s.sourceClient.CoreV1().RESTClient().Post().
		Resource("pods").
		Name(nameToSource(s.subclusterName, podNamespace, podName)).
		Namespace(s.sourceNamespace).
		SubResource("attach")

	attachOptions := &corev1.PodAttachOptions{
		Container: container,
		TTY:       tty,
		Stdin:     in != nil,
		Stdout:    out != nil,
		Stderr:    errOut != nil,
	}

	req.VersionedParams(attachOptions, scheme.ParameterCodec)

	executor, err := remotecommandclient.NewSPDYExecutor(s.sourceRestConfig, http.MethodPost, req.URL())
	if err != nil {
		return fmt.Errorf("unable to create executor: %v", err)
	}

	err = executor.StreamWithContext(ctx, remotecommandclient.StreamOptions{
		Stdin:             in,
		Stdout:            out,
		Stderr:            errOut,
		Tty:               tty,
		TerminalSizeQueue: newTranslatorSizeQueue(resize),
	})
	if err != nil {
		return fmt.Errorf("unable to stream: %v", err)
	}
	return nil
}

func (s *Server) getAttach(request *restful.Request, response *restful.Response) {
	params := getExecRequestParams(request)

	streamOpts, err := remotecommandserver.NewOptions(request.Request)
	if err != nil {
		_ = response.WriteError(http.StatusBadRequest, err)
		return
	}

	remotecommandserver.ServeAttach(
		response.ResponseWriter,
		request.Request,
		s,
		params.podName+"/"+params.podNamespace,
		params.podUID,
		params.containerName,
		streamOpts,
		s.idleTimeout,
		s.streamCreationTimeout,
		remotecommandconsts.SupportedStreamingProtocols,
	)
}
