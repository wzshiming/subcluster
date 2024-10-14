package sublet

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"time"

	"github.com/emicklei/go-restful/v3"
	"github.com/gorilla/handlers"
	"github.com/wzshiming/cmux"
	"github.com/wzshiming/cmux/pattern"
	"k8s.io/apimachinery/pkg/types"
	remotecommandconsts "k8s.io/apimachinery/pkg/util/remotecommand"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	remotecommandclient "k8s.io/client-go/tools/remotecommand"
)

func getHandlerForDisabledEndpoint(errorMessage string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, errorMessage, http.StatusMethodNotAllowed)
	}
}

const (
	pprofBasePath = "/debug/pprof/"
)

var disableHandler = getHandlerForDisabledEndpoint("Debug endpoints are disabled.")

type Server struct {
	sourceClient          kubernetes.Interface
	restfulCont           *restful.Container
	sourceRestConfig      *rest.Config
	idleTimeout           time.Duration
	streamCreationTimeout time.Duration

	subclusterName  string
	sourceNamespace string
}

type ServerConfig struct {
	SourceClient     kubernetes.Interface
	SourceRestConfig *rest.Config
	SubclusterName   string
	SourceNamespace  string
}

func NewServer(conf ServerConfig) *Server {
	return &Server{
		sourceClient:          conf.SourceClient,
		sourceRestConfig:      conf.SourceRestConfig,
		restfulCont:           restful.NewContainer(),
		subclusterName:        conf.SubclusterName,
		sourceNamespace:       conf.SourceNamespace,
		idleTimeout:           1 * time.Hour,
		streamCreationTimeout: remotecommandconsts.DefaultStreamCreationTimeout,
	}
}

// InstallDebuggingDisabledHandlers registers the HTTP request patterns that provide better error message
func (s *Server) InstallDebuggingDisabledHandlers() {
	paths := []string{
		"/run/", "/exec/", "/attach/", "/portForward/", "/containerLogs/",
		"/runningpods/", pprofBasePath, "/logs/"}
	for _, p := range paths {
		s.restfulCont.Handle(p, disableHandler)
	}
}

// InstallDebuggingHandlers registers the HTTP request patterns that provide debugging functionality
func (s *Server) InstallDebuggingHandlers() {
	// TODO: These interface control planes are not used for now, so don't implement them first
	paths := []string{
		"/run/", "/runningpods/", "/logs/"}
	for _, p := range paths {
		s.restfulCont.Handle(p, disableHandler)
	}

	ws := new(restful.WebService)
	ws.
		Path("/attach")
	ws.Route(ws.GET("/{podNamespace}/{podID}/{containerName}").
		To(s.getAttach).
		Operation("getAttach"))
	ws.Route(ws.POST("/{podNamespace}/{podID}/{containerName}").
		To(s.getAttach).
		Operation("getAttach"))
	ws.Route(ws.GET("/{podNamespace}/{podID}/{uid}/{containerName}").
		To(s.getAttach).
		Operation("getAttach"))
	ws.Route(ws.POST("/{podNamespace}/{podID}/{uid}/{containerName}").
		To(s.getAttach).
		Operation("getAttach"))
	s.restfulCont.Add(ws)

	ws = new(restful.WebService)
	ws.
		Path("/exec")
	ws.Route(ws.GET("/{podNamespace}/{podID}/{containerName}").
		To(s.getExec).
		Operation("getExec"))
	ws.Route(ws.POST("/{podNamespace}/{podID}/{containerName}").
		To(s.getExec).
		Operation("getExec"))
	ws.Route(ws.GET("/{podNamespace}/{podID}/{uid}/{containerName}").
		To(s.getExec).
		Operation("getExec"))
	ws.Route(ws.POST("/{podNamespace}/{podID}/{uid}/{containerName}").
		To(s.getExec).
		Operation("getExec"))
	s.restfulCont.Add(ws)

	ws = new(restful.WebService)
	ws.
		Path("/portForward")
	ws.Route(ws.GET("/{podNamespace}/{podID}").
		To(s.getPortForward).
		Operation("getPortForward"))
	ws.Route(ws.POST("/{podNamespace}/{podID}").
		To(s.getPortForward).
		Operation("getPortForward"))
	ws.Route(ws.GET("/{podNamespace}/{podID}/{uid}").
		To(s.getPortForward).
		Operation("getPortForward"))
	ws.Route(ws.POST("/{podNamespace}/{podID}/{uid}").
		To(s.getPortForward).
		Operation("getPortForward"))
	s.restfulCont.Add(ws)

	ws = new(restful.WebService)
	ws.
		Path("/containerLogs")
	ws.Route(ws.GET("/{podNamespace}/{podID}/{containerName}").
		To(s.getContainerLogs).
		Operation("getContainerLogs"))
	s.restfulCont.Add(ws)
}

// Run runs the specified Server.
// This should never exit.
func (s *Server) Run(ctx context.Context, address string, certFile, privateKeyFile string) error {

	listener, err := net.Listen("tcp", address)
	if err != nil {
		return err
	}

	muxListener := cmux.NewMuxListener(listener)
	tlsListener, err := muxListener.MatchPrefix(pattern.Pattern[pattern.TLS]...)
	if err != nil {
		return fmt.Errorf("match tls listener: %w", err)
	}
	unmatchedListener, err := muxListener.Unmatched()
	if err != nil {
		return fmt.Errorf("unmatched listener: %w", err)
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	errCh := make(chan error, 1)

	handler := handlers.LoggingHandler(os.Stderr, s.restfulCont)

	if certFile != "" && privateKeyFile != "" {
		go func() {
			svc := &http.Server{
				ReadHeaderTimeout: 5 * time.Second,
				BaseContext: func(_ net.Listener) context.Context {
					return ctx
				},
				Addr:    address,
				Handler: handler,
			}
			err = svc.ServeTLS(tlsListener, certFile, privateKeyFile)
			if err != nil {
				errCh <- fmt.Errorf("serve https: %w", err)
			}
		}()
	} else {
		svc := httptest.Server{
			Listener: tlsListener,
			Config: &http.Server{
				ReadHeaderTimeout: 5 * time.Second,
				BaseContext: func(_ net.Listener) context.Context {
					return ctx
				},
				Addr:    address,
				Handler: handler,
			},
		}
		svc.StartTLS()
	}

	go func() {
		svc := &http.Server{
			ReadHeaderTimeout: 5 * time.Second,
			BaseContext: func(_ net.Listener) context.Context {
				return ctx
			},
			Addr:    address,
			Handler: handler,
		}
		err = svc.Serve(unmatchedListener)
		if err != nil {
			errCh <- fmt.Errorf("serve http: %w", err)
		}
	}()

	select {
	case err = <-errCh:
	case <-ctx.Done():
		err = ctx.Err()
	}

	return err
}

type execRequestParams struct {
	podNamespace  string
	podName       string
	podUID        types.UID
	containerName string
	cmd           []string
}

func getExecRequestParams(req *restful.Request) execRequestParams {
	return execRequestParams{
		podNamespace:  req.PathParameter("podNamespace"),
		podName:       req.PathParameter("podID"),
		podUID:        types.UID(req.PathParameter("uid")),
		containerName: req.PathParameter("containerName"),
		cmd:           req.Request.URL.Query()["command"],
	}
}

type translatorSizeQueue struct {
	resizeChan <-chan remotecommandclient.TerminalSize
}

func newTranslatorSizeQueue(resizeChan <-chan remotecommandclient.TerminalSize) *translatorSizeQueue {
	if resizeChan == nil {
		return nil
	}
	return &translatorSizeQueue{resizeChan}
}

func (t *translatorSizeQueue) Next() *remotecommandclient.TerminalSize {
	size, ok := <-t.resizeChan
	if !ok {
		return nil
	}
	return &size
}
