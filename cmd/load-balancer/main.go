package main

import (
	"flag"
	"fmt"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"strings"

	"github.com/manojks1999/load-balancer/pkg/config"
	"github.com/manojks1999/load-balancer/pkg/domain"
	"github.com/manojks1999/load-balancer/pkg/health"
	"github.com/manojks1999/load-balancer/pkg/strategy"
	log "github.com/sirupsen/logrus"
)

var (
	port       = flag.Int("port", 8080, "where to start load-baalancer")
	configFile = flag.String("config-path", "", "The config file to supply to load-baalancer")
)

type LoadBalancer struct {
	// Config is the configuration loaded from a config file
	// TODO(manojks1999): This could be improved, as to fetch the configuration from
	// a more abstract concept (like ConfigSource) that can either be a file or
	// something else, and also should support hot reloading.
	Config *config.Config

	// ServerList will contain a mapping between matcher and replicas
	ServerList map[string]*config.ServerList
}

func NewLoadBalancer(conf *config.Config) *LoadBalancer {
	// TODO(manojks1999): prevent multiple or invalid matchers before creating the
	// server
	serverMap := make(map[string]*config.ServerList, 0)

	for _, service := range conf.Services {
		servers := make([]*domain.Server, 0)
		for _, replica := range service.Replicas {
			ur, err := url.Parse(replica.Url)
			if err != nil {
				log.Fatal(err)
			}
			proxy := httputil.NewSingleHostReverseProxy(ur)
			servers = append(servers, &domain.Server{
				Url:      ur,
				Proxy:    proxy,
				Metadata: replica.Metadata,
			})
		}
		checker, err := health.NewChecker(nil, servers)
		if err != nil {
			log.Fatal(err)
		}
		serverMap[service.Matcher] = &config.ServerList{
			Servers:  servers,
			Name:     service.Name,
			Strategy: strategy.LoadStrategy(service.Strategy),
			Hc:       checker,
		}
	}
	// start all the health checkers for all provided matchers
	for _, sl := range serverMap {
		go sl.Hc.Start()
	}
	return &LoadBalancer{
		Config:     conf,
		ServerList: serverMap,
	}
}

// Looks for the first server list that matches the reqPath (i.e matcher)
// Will return an error if no matcher have been found.
// TODO(manojks1999): Does it make sense to allow default responders?
func (f *LoadBalancer) findServiceList(reqPath string) (*config.ServerList, error) {
	log.Infof("Trying to find matcher for request '%s'", reqPath)
	for matcher, s := range f.ServerList {
		if strings.HasPrefix(reqPath, matcher) {
			log.Infof("Found service '%s' matching the request", s.Name)
			return s, nil
		}
	}
	return nil, fmt.Errorf("Could not find a matcher for url: '%s'", reqPath)
}

func (f *LoadBalancer) ServeHTTP(res http.ResponseWriter, req *http.Request) {
	// TODO(manojks1999): We need to support per service forwarding, i.e this method
	// should read the request path, say host:port/serice/rest/of/url this should
	// be load balanced against service named "service" and url will be
	// "host{i}:port{i}/rest/of/url
	log.Infof("Received new request: url='%s'", req.Host)
	sl, err := f.findServiceList(req.URL.Path)
	if err != nil {
		log.Error(err)
		res.WriteHeader(http.StatusNotFound)
		return
	}

	next, err := sl.Strategy.Next(sl.Servers)

	if err != nil {
		log.Error(err)
		res.WriteHeader(http.StatusInternalServerError)
		return
	}

	log.Infof("Forwarding to the server='%s'", next.Url.Host)
	next.Forward(res, req)
}

func main() {
	flag.Parse()
	file, err := os.Open(*configFile)
	if err != nil {
		log.Fatal(err)
	}
	defer file.Close()
	conf, err := config.LoadConfig(file)

	if err != nil {
		log.Fatal(err)
	}

	LoadBalancer := NewLoadBalancer(conf)

	server := http.Server{
		Addr:    fmt.Sprintf(":%d", *port),
		Handler: LoadBalancer,
	}

	if err := server.ListenAndServe(); err != nil {
		log.Fatal(err)
	}
}
