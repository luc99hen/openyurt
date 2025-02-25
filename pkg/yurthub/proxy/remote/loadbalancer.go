/*
Copyright 2020 The OpenYurt Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package remote

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sync"

	"k8s.io/apimachinery/pkg/runtime/schema"
	apirequest "k8s.io/apiserver/pkg/endpoints/request"
	"k8s.io/klog/v2"

	yurtutil "github.com/openyurtio/openyurt/pkg/util"
	"github.com/openyurtio/openyurt/pkg/yurthub/cachemanager"
	"github.com/openyurtio/openyurt/pkg/yurthub/filter"
	"github.com/openyurtio/openyurt/pkg/yurthub/healthchecker"
	"github.com/openyurtio/openyurt/pkg/yurthub/proxy/util"
	"github.com/openyurtio/openyurt/pkg/yurthub/transport"
	hubutil "github.com/openyurtio/openyurt/pkg/yurthub/util"
)

type loadBalancerAlgo interface {
	PickOne() *util.RemoteProxy
	Name() string
}

type rrLoadBalancerAlgo struct {
	sync.Mutex
	checker  healthchecker.MultipleBackendsHealthChecker
	backends []*util.RemoteProxy
	next     int
}

func (rr *rrLoadBalancerAlgo) Name() string {
	return "rr algorithm"
}

func (rr *rrLoadBalancerAlgo) PickOne() *util.RemoteProxy {
	if len(rr.backends) == 0 {
		return nil
	} else if len(rr.backends) == 1 {
		if rr.checker.BackendHealthyStatus(rr.backends[0].RemoteServer()) {
			return rr.backends[0]
		}
		return nil
	} else {
		// round robin
		rr.Lock()
		defer rr.Unlock()
		hasFound := false
		selected := rr.next
		for i := 0; i < len(rr.backends); i++ {
			selected = (rr.next + i) % len(rr.backends)
			if rr.checker.BackendHealthyStatus(rr.backends[selected].RemoteServer()) {
				hasFound = true
				break
			}
		}

		if hasFound {
			rr.next = (selected + 1) % len(rr.backends)
			return rr.backends[selected]
		}
	}

	return nil
}

type priorityLoadBalancerAlgo struct {
	sync.Mutex
	checker  healthchecker.MultipleBackendsHealthChecker
	backends []*util.RemoteProxy
}

func (prio *priorityLoadBalancerAlgo) Name() string {
	return "priority algorithm"
}

func (prio *priorityLoadBalancerAlgo) PickOne() *util.RemoteProxy {
	if len(prio.backends) == 0 {
		return nil
	} else if len(prio.backends) == 1 {
		if prio.checker.BackendHealthyStatus(prio.backends[0].RemoteServer()) {
			return prio.backends[0]
		}
		return nil
	} else {
		prio.Lock()
		defer prio.Unlock()
		for i := 0; i < len(prio.backends); i++ {
			if prio.checker.BackendHealthyStatus(prio.backends[i].RemoteServer()) {
				return prio.backends[i]
			}
		}

		return nil
	}
}

// LoadBalancer is an interface for proxying http request to remote server
// based on the load balance mode(round-robin or priority)
type LoadBalancer interface {
	ServeHTTP(rw http.ResponseWriter, req *http.Request)
}

type loadBalancer struct {
	backends      []*util.RemoteProxy
	algo          loadBalancerAlgo
	localCacheMgr cachemanager.CacheManager
	filterFinder  filter.FilterFinder
	stopCh        <-chan struct{}
}

// NewLoadBalancer creates a loadbalancer for specified remote servers
func NewLoadBalancer(
	lbMode string,
	remoteServers []*url.URL,
	localCacheMgr cachemanager.CacheManager,
	transportMgr transport.Interface,
	healthChecker healthchecker.MultipleBackendsHealthChecker,
	filterFinder filter.FilterFinder,
	stopCh <-chan struct{}) (LoadBalancer, error) {
	lb := &loadBalancer{
		localCacheMgr: localCacheMgr,
		filterFinder:  filterFinder,
		stopCh:        stopCh,
	}
	backends := make([]*util.RemoteProxy, 0, len(remoteServers))
	for i := range remoteServers {
		b, err := util.NewRemoteProxy(remoteServers[i], lb.modifyResponse, lb.errorHandler, transportMgr, stopCh)
		if err != nil {
			klog.Errorf("could not new proxy backend(%s), %v", remoteServers[i].String(), err)
			continue
		}
		backends = append(backends, b)
	}
	if len(backends) == 0 {
		return nil, fmt.Errorf("no backends can be used by lb")
	}

	var algo loadBalancerAlgo
	switch lbMode {
	case "rr":
		algo = &rrLoadBalancerAlgo{backends: backends, checker: healthChecker}
	case "priority":
		algo = &priorityLoadBalancerAlgo{backends: backends, checker: healthChecker}
	default:
		algo = &rrLoadBalancerAlgo{backends: backends, checker: healthChecker}
	}

	lb.backends = backends
	lb.algo = algo

	return lb, nil
}

func (lb *loadBalancer) ServeHTTP(rw http.ResponseWriter, req *http.Request) {
	// pick a remote proxy based on the load balancing algorithm.
	rp := lb.algo.PickOne()
	if rp == nil {
		// exceptional case
		klog.Errorf("could not pick one healthy backends by %s for request %s", lb.algo.Name(), hubutil.ReqString(req))
		http.Error(rw, "could not pick one healthy backends, try again to go through local proxy.", http.StatusInternalServerError)
		return
	}
	klog.V(3).Infof("picked backend %s by %s for request %s", rp.Name(), lb.algo.Name(), hubutil.ReqString(req))

	rp.ServeHTTP(rw, req)
}

func (lb *loadBalancer) errorHandler(rw http.ResponseWriter, req *http.Request, err error) {
	klog.Errorf("remote proxy error handler: %s, %v", hubutil.ReqString(req), err)
	if lb.localCacheMgr == nil || !lb.localCacheMgr.CanCacheFor(req) {
		rw.WriteHeader(http.StatusBadGateway)
		return
	}

	ctx := req.Context()
	if info, ok := apirequest.RequestInfoFrom(ctx); ok {
		if info.Verb == "get" || info.Verb == "list" {
			if obj, err := lb.localCacheMgr.QueryCache(req); err == nil {
				hubutil.WriteObject(http.StatusOK, obj, rw, req)
				return
			}
		}
	}
	rw.WriteHeader(http.StatusBadGateway)
}

func (lb *loadBalancer) modifyResponse(resp *http.Response) error {
	if resp == nil || resp.Request == nil {
		klog.Infof("no request info in response, skip cache response")
		return nil
	}

	req := resp.Request
	ctx := req.Context()

	// re-added transfer-encoding=chunked response header for watch request
	info, exists := apirequest.RequestInfoFrom(ctx)
	if exists {
		if info.Verb == "watch" {
			klog.V(5).Infof("add transfer-encoding=chunked header into response for req %s", hubutil.ReqString(req))
			h := resp.Header
			if hv := h.Get("Transfer-Encoding"); hv == "" {
				h.Add("Transfer-Encoding", "chunked")
			}
		}
	}

	// wrap response for tracing traffic information of requests
	resp = hubutil.WrapWithTrafficTrace(req, resp)

	if resp.StatusCode >= http.StatusOK && resp.StatusCode <= http.StatusPartialContent {
		// prepare response content type
		reqContentType, _ := hubutil.ReqContentTypeFrom(ctx)
		respContentType := resp.Header.Get(yurtutil.HttpHeaderContentType)
		if len(respContentType) == 0 {
			respContentType = reqContentType
		}
		ctx = hubutil.WithRespContentType(ctx, respContentType)
		req = req.WithContext(ctx)

		// filter response data
		if responseFilter, ok := lb.filterFinder.FindResponseFilter(req); ok {
			wrapBody, needUncompressed := hubutil.NewGZipReaderCloser(resp.Header, resp.Body, req, "filter")
			size, filterRc, err := responseFilter.Filter(req, wrapBody, lb.stopCh)
			if err != nil {
				klog.Errorf("could not filter response for %s, %v", hubutil.ReqString(req), err)
				return err
			}
			resp.Body = filterRc
			if size > 0 {
				resp.ContentLength = int64(size)
				resp.Header.Set(yurtutil.HttpHeaderContentLength, fmt.Sprint(size))
			}

			// after gunzip in filter, the header content encoding should be removed.
			// because there's no need to gunzip response.body again.
			if needUncompressed {
				resp.Header.Del("Content-Encoding")
			}
		}

		if !yurtutil.IsNil(lb.localCacheMgr) {
			// cache resp with storage interface
			lb.cacheResponse(req, resp)
		}
	} else if resp.StatusCode == http.StatusNotFound && info.Verb == "list" && lb.localCacheMgr != nil {
		// 404 Not Found: The CRD may have been unregistered and should be updated locally as well.
		// Other types of requests may return a 404 response for other reasons (for example, getting a pod that doesn't exist).
		// And the main purpose is to return 404 when list an unregistered resource locally, so here only consider the list request.
		gvr := schema.GroupVersionResource{
			Group:    info.APIGroup,
			Version:  info.APIVersion,
			Resource: info.Resource,
		}

		err := lb.localCacheMgr.DeleteKindFor(gvr)
		if err != nil {
			klog.Errorf("failed: %v", err)
		}
	}
	return nil
}

func (lb *loadBalancer) cacheResponse(req *http.Request, resp *http.Response) {
	if lb.localCacheMgr.CanCacheFor(req) {
		wrapPrc, needUncompressed := hubutil.NewGZipReaderCloser(resp.Header, resp.Body, req, "cache-manager")
		// after gunzip in filter, the header content encoding should be removed.
		// because there's no need to gunzip response.body again.
		if needUncompressed {
			resp.Header.Del("Content-Encoding")
		}
		resp.Body = wrapPrc

		// cache the response at local.
		rc, prc := hubutil.NewDualReadCloser(req, resp.Body, true)
		go func(req *http.Request, prc io.ReadCloser, stopCh <-chan struct{}) {
			if err := lb.localCacheMgr.CacheResponse(req, prc, stopCh); err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, context.Canceled) {
				klog.Errorf("lb could not cache req %s in local cache, %v", hubutil.ReqString(req), err)
			}
		}(req, prc, req.Context().Done())
		resp.Body = rc
	}
}
