package prometheus

import (
	"fmt"
	"net/http"

	"github.com/n42blockchain/N42/log"
)

var EnabledExpensive = false

// Setup starts a dedicated metrics server at the given address.
// This function enables metrics reporting separate from pprof.
func Setup(address string, log log.Logger) *http.ServeMux {
	mux := http.NewServeMux()
	mux.Handle("/debug/metrics/prometheus", Handler(DefaultRegistry))

	server := &http.Server{
		Addr:    address,
		Handler: mux,
	}

	go func() {
		if err := server.ListenAndServe(); err != nil {
			log.Error("Failure in running Prometheus server", "err", err)
		}
	}()

	log.Info("Enabling metrics export to prometheus", "path", fmt.Sprintf("http://%s/debug/metrics/prometheus", address))

	return mux
}
