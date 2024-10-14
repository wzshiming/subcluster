package sublet

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/emicklei/go-restful/v3"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/portforward"
	"k8s.io/client-go/transport/spdy"
	portforwardserver "k8s.io/kubelet/pkg/cri/streaming/portforward"
)

// PortForward handles a port forwarding request.
func (s *Server) PortForward(ctx context.Context, name string, uid types.UID, port int32, stream io.ReadWriteCloser) error {
	defer func() {
		_ = stream.Close()
	}()

	pod := strings.Split(name, "/")
	if len(pod) != 2 {
		return fmt.Errorf("invalid pod name %q", name)
	}
	podName, podNamespace := pod[0], pod[1]

	req := s.sourceClient.CoreV1().RESTClient().Post().
		Resource("pods").
		Name(nameToSource(s.subclusterName, podNamespace, podName)).
		Namespace(s.sourceNamespace).
		SubResource("portforward")

	req.VersionedParams(&corev1.PodPortForwardOptions{
		Ports: []int32{port},
	}, scheme.ParameterCodec)

	roundTripper, upgrader, err := spdy.RoundTripperFor(s.sourceRestConfig)
	if err != nil {
		return err
	}
	dialer := spdy.NewDialer(upgrader, &http.Client{Transport: roundTripper}, http.MethodPost, req.URL())

	streamConn, _, err := dialer.Dial(portforward.PortForwardProtocolV1Name)
	if err != nil {
		return err
	}

	defer streamConn.Close()

	requestID := 0

	// create error stream
	headers := http.Header{}
	headers.Set(corev1.StreamType, corev1.StreamTypeError)
	headers.Set(corev1.PortHeader, fmt.Sprintf("%d", port))
	headers.Set(corev1.PortForwardRequestIDHeader, strconv.Itoa(requestID))
	errorStream, err := streamConn.CreateStream(headers)
	if err != nil {
		return err
	}
	// we're not writing to this stream
	errorStream.Close()
	defer streamConn.RemoveStreams(errorStream)

	errorChan := make(chan error)
	go func() {
		message, err := io.ReadAll(errorStream)
		switch {
		case err != nil:
			errorChan <- fmt.Errorf("error reading from error stream for port: %v", err)
		case len(message) > 0:
			errorChan <- fmt.Errorf("an error occurred forwarding: %v", string(message))
		}
		close(errorChan)
	}()

	// create data stream
	headers.Set(corev1.StreamType, corev1.StreamTypeData)
	dataStream, err := streamConn.CreateStream(headers)
	if err != nil {
		return fmt.Errorf("error creating forwarding stream for port: %v", err)
	}
	defer streamConn.RemoveStreams(dataStream)

	localError := make(chan struct{})
	remoteDone := make(chan struct{})

	go func() {
		// Copy from the remote side to the local port.
		if _, err := io.Copy(stream, dataStream); err != nil && !strings.Contains(err.Error(), "use of closed network connection") {
			runtime.HandleError(fmt.Errorf("error copying from remote stream to local connection: %v", err))
		}

		// inform the select below that the remote copy is done
		close(remoteDone)
	}()

	go func() {
		// inform server we're not sending any more data after copy unblocks
		defer dataStream.Close()

		// Copy from the local port to the remote side.
		if _, err := io.Copy(dataStream, stream); err != nil && !strings.Contains(err.Error(), "use of closed network connection") {
			runtime.HandleError(fmt.Errorf("error copying from local connection to remote stream: %v", err))
			// break out of the select below without waiting for the other copy to finish
			close(localError)
		}
	}()

	// wait for either a local->remote error or for copying from remote->local to finish
	select {
	case <-remoteDone:
	case <-localError:
	case <-ctx.Done():
	}

	// always expect something on errorChan (it may be nil)
	err = <-errorChan
	if err != nil {
		return err
	}

	return nil
}

// getPortForward handles a new restful port forward request. It determines the
// pod name and uid and then calls ServePortForward.
func (s *Server) getPortForward(req *restful.Request, resp *restful.Response) {
	params := getPortForwardRequestParams(req)

	portForwardOptions, err := portforwardserver.NewV4Options(req.Request)
	if err != nil {
		_ = resp.WriteError(http.StatusBadRequest, err)
		return
	}

	portforwardserver.ServePortForward(
		resp.ResponseWriter,
		req.Request,
		s,
		params.podName+"/"+params.podNamespace,
		params.podUID,
		portForwardOptions,
		s.idleTimeout,
		s.streamCreationTimeout,
		portforwardserver.SupportedProtocols,
	)
}

type portForwardRequestParams struct {
	podNamespace string
	podName      string
	podUID       types.UID
}

func getPortForwardRequestParams(req *restful.Request) portForwardRequestParams {
	return portForwardRequestParams{
		podNamespace: req.PathParameter("podNamespace"),
		podName:      req.PathParameter("podID"),
		podUID:       types.UID(req.PathParameter("uid")),
	}
}
