package plugin

import (
	"net/http"

	"github.com/thehaven/docker-net-dhcp/pkg/util"
)

// EndpointInterface represents a network interface
type EndpointInterface struct {
	Address     string `json:",omitempty"`
	AddressIPv6 string `json:",omitempty"`
	MacAddress  string `json:",omitempty"`
}

// CreateEndpointRequest is the request to create an endpoint
type CreateEndpointRequest struct {
	NetworkID  string
	EndpointID string
	Interface  *EndpointInterface
	Options    map[string]interface{}
}

// CreateEndpointResponse is the response to creating an endpoint
type CreateEndpointResponse struct {
	Interface *EndpointInterface `json:",omitempty"`
}

// InfoRequest is the request for information about an endpoint
type InfoRequest struct {
	NetworkID  string
	EndpointID string
}

// InfoResponse is the response to an info request
type InfoResponse struct {
	Value map[string]interface{}
}

// DeleteEndpointRequest is the request to delete an endpoint
type DeleteEndpointRequest struct {
	NetworkID  string
	EndpointID string
}

// InterfaceName represents the name of an interface
type InterfaceName struct {
	SrcName   string
	DstPrefix string
}

// StaticRoute represents a static route
type StaticRoute struct {
	Destination string
	NextHop     string
	RouteType   int
}

// JoinRequest is the request to join a container to an endpoint
type JoinRequest struct {
	NetworkID  string
	EndpointID string
	SandboxKey string
	Options    map[string]interface{}
}

// JoinResponse is the response to a join request
type JoinResponse struct {
	InterfaceName InterfaceName `json:",omitempty"`
	Gateway       string        `json:",omitempty"`
	GatewayIPv6   string        `json:",omitempty"`
	StaticRoutes  []*StaticRoute `json:",omitempty"`
}

// LeaveRequest is the request to remove a container from an endpoint
type LeaveRequest struct {
	NetworkID  string
	EndpointID string
}

// IPv4Data contains IPv4-related network information
type IPv4Data struct {
	AddressSpace string
	Pool         string
	Gateway      string
	AuxAddresses map[string]string
}

// CreateNetworkRequest is the request to create a network
type CreateNetworkRequest struct {
	NetworkID string
	Options   map[string]interface{}
	IPv4Data  []IPv4Data
	IPv6Data  []IPv4Data
}

// DeleteNetworkRequest is the request to delete a network
type DeleteNetworkRequest struct {
	NetworkID string
}

func (p *Plugin) apiGetCapabilities(w http.ResponseWriter, r *http.Request) {
	util.JSONResponse(w, map[string]string{
		"Scope": "local",
	}, http.StatusOK)
}

func (p *Plugin) apiCreateEndpoint(w http.ResponseWriter, r *http.Request) {
	var req CreateEndpointRequest
	if err := util.ParseJSONBody(&req, w, r); err != nil {
		return
	}

	res, err := p.CreateEndpoint(r.Context(), req)
	if err != nil {
		util.JSONErrResponse(w, err, 0)
		return
	}

	util.JSONResponse(w, res, http.StatusOK)
}

func (p *Plugin) apiEndpointOperInfo(w http.ResponseWriter, r *http.Request) {
	var req InfoRequest
	if err := util.ParseJSONBody(&req, w, r); err != nil {
		return
	}

	res, err := p.EndpointOperInfo(r.Context(), req)
	if err != nil {
		util.JSONErrResponse(w, err, 0)
		return
	}

	util.JSONResponse(w, res, http.StatusOK)
}

func (p *Plugin) apiDeleteEndpoint(w http.ResponseWriter, r *http.Request) {
	var req DeleteEndpointRequest
	if err := util.ParseJSONBody(&req, w, r); err != nil {
		return
	}

	if err := p.DeleteEndpoint(req); err != nil {
		util.JSONErrResponse(w, err, 0)
		return
	}

	util.JSONResponse(w, map[string]string{}, http.StatusOK)
}

func (p *Plugin) apiJoin(w http.ResponseWriter, r *http.Request) {
	var req JoinRequest
	if err := util.ParseJSONBody(&req, w, r); err != nil {
		return
	}

	res, err := p.Join(r.Context(), req)
	if err != nil {
		util.JSONErrResponse(w, err, 0)
		return
	}

	util.JSONResponse(w, res, http.StatusOK)
}

func (p *Plugin) apiLeave(w http.ResponseWriter, r *http.Request) {
	var req LeaveRequest
	if err := util.ParseJSONBody(&req, w, r); err != nil {
		return
	}

	if err := p.Leave(r.Context(), req); err != nil {
		util.JSONErrResponse(w, err, 0)
		return
	}

	util.JSONResponse(w, map[string]string{}, http.StatusOK)
}

func (p *Plugin) apiCreateNetwork(w http.ResponseWriter, r *http.Request) {
	var req CreateNetworkRequest
	if err := util.ParseJSONBody(&req, w, r); err != nil {
		return
	}

	if err := p.CreateNetwork(req); err != nil {
		util.JSONErrResponse(w, err, 0)
		return
	}

	util.JSONResponse(w, map[string]string{}, http.StatusOK)
}

func (p *Plugin) apiDeleteNetwork(w http.ResponseWriter, r *http.Request) {
	var req DeleteNetworkRequest
	if err := util.ParseJSONBody(&req, w, r); err != nil {
		return
	}

	if err := p.DeleteNetwork(req); err != nil {
		util.JSONErrResponse(w, err, 0)
		return
	}

	util.JSONResponse(w, map[string]string{}, http.StatusOK)
}
