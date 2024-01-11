package main

// Not all standard libraries may work so check the features you are using
// and the following URL before proceeding:
// https://tinygo.org/docs/reference/lang-support/stdlib/
import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	// For high performance you may want to consider replacing the
	// default memory allocatory for tinygo as some has suggested that under
	// load nottinygc performs better
	//_ "github.com/wasilibs/nottinygc"

	"github.com/tetratelabs/proxy-wasm-go-sdk/proxywasm"
	"github.com/tetratelabs/proxy-wasm-go-sdk/proxywasm/types"
	// the econding/json std library can be imported but not all of the tests
	// pass with tinygo so you could also use an alternative like gjson
	"github.com/tidwall/gjson"
)

var (
	// If the W3C traceparent and the istio x-request-id header are not present just send a generic id
	defaultTraceID      = "00-0aa0000000aa00aa0000aa000a00000a-a0aa0a0000000000-00"
	defaultProblemTitle = "service mesh returned an error"
	// The following URIs will be sent for the type field in the JSON payload
	defaultProblemTypeURIMap = map[string]string{
		"400": "https://datatracker.ietf.org/html/rfc9110#section-15.5.1",
		"401": "https://datatracker.ietf.org/html/rfc9110#section-15.5.2",
		"403": "https://datatracker.ietf.org/html/rfc9110#section-15.5.4",
		"404": "https://datatracker.ietf.org/html/rfc9110#section-15.5.5",
		"405": "https://datatracker.ietf.org/html/rfc9110#section-15.5.6",
		"406": "https://datatracker.ietf.org/html/rfc9110#section-15.5.7",
		"408": "https://datatracker.ietf.org/html/rfc9110#section-15.5.9",
		"409": "https://datatracker.ietf.org/html/rfc9110#section-15.5.10",
		"412": "https://datatracker.ietf.org/html/rfc9110#section-15.5.13",
		"415": "https://datatracker.ietf.org/html/rfc9110#section-15.5.16",
		"422": "https://datatracker.ietf.org/html/rfc4918#section-11.2",
		"426": "https://datatracker.ietf.org/html/rfc9110#section-15.5.22",
		"500": "https://datatracker.ietf.org/html/rfc9110#section-15.6.1",
		"502": "https://datatracker.ietf.org/html/rfc9110#section-15.6.3",
		"503": "https://datatracker.ietf.org/html/rfc9110#section-15.6.4",
		"504": "https://datatracker.ietf.org/html/rfc9110#section-15.6.5",
	}
	// This will only be used if there is no mapping for the status code
	default4xxProblemTypeURI = "https://datatracker.ietf.org/doc/html/rfc9110#name-client-error-4xx"
	default5xxProblemTypeURI = "https://datatracker.ietf.org/doc/html/rfc9110#name-server-error-5xx"
)

// -------------------- NOTES--------------------
// This plugin only works with http 2.0 because Istio requires http 2.0 and will send an
// 426 status code "upgrade required"
// -------------------- NOTES--------------------

func main() {
	// SetVMContext is the entrypoint for setting up this entire Wasm VM.
	// Please make sure that this entrypoint be called during "main()" function,
	// otherwise this VM would fail.
	proxywasm.SetVMContext(&vmContext{})
}

// vmContext implements types.VMContext interface of proxy-wasm-go SDK.
type vmContext struct {
	// Embed the default VM context here,
	// so that we don't need to reimplement all the methods.
	types.DefaultVMContext
}

// Override types.DefaultVMContext.
func (*vmContext) NewPluginContext(contextID uint32) types.PluginContext {
	return &pluginContext{}
}

// pluginContext implements types.PluginContext interface of proxy-wasm-go SDK.
type pluginContext struct {
	// Embed the default plugin context here,
	// so that we don't need to reimplement all the methods.
	types.DefaultPluginContext
	configuration pluginConfiguration
}

// pluginConfiguration is a type to represent an example configuration for this wasm plugin.
type pluginConfiguration struct {
	// Only modify responses for specific endpoint prefixes
	targetURLPrefixes []string
	// Only modify response status code >= startStatusCode && <= endStatusCode
	// The plugin will default to startStatusCode = 400 and endStatusCode = 599
	startStatusCode   int
	endStatusCode     int
	problemTypeURIMap map[string]string
	// Defaults to "service mesh returned an error"
	problemTitle string
}

// Override types.DefaultPluginContext.
func (ctx *pluginContext) OnPluginStart(pluginConfigurationSize int) types.OnPluginStartStatus {
	data, err := proxywasm.GetPluginConfiguration()
	if err != nil && err != types.ErrorStatusNotFound {
		proxywasm.LogCriticalf("error reading plugin configuration: %v", err)
		return types.OnPluginStartStatusFailed
	}
	config, err := parsePluginConfiguration(data)
	if err != nil {
		proxywasm.LogCriticalf("error parsing plugin configuration: %v", err)
		return types.OnPluginStartStatusFailed
	}
	ctx.configuration = config
	return types.OnPluginStartStatusOK
}

// parsePluginConfiguration parses the json plugin confiuration data and returns pluginConfiguration.
// Note that this parses the json data by gjson, since TinyGo doesn't support encoding/json.
// You can also try https://github.com/mailru/easyjson, which supports decoding to a struct.
func parsePluginConfiguration(data []byte) (pluginConfiguration, error) {
	if len(data) == 0 {
		return pluginConfiguration{}, nil
	}

	config := &pluginConfiguration{}
	if !gjson.ValidBytes(data) {
		return pluginConfiguration{}, fmt.Errorf("the plugin configuration is not a valid json: %q", string(data))
	}

	jsonData := gjson.ParseBytes(data)
	targetURLPrefixes := jsonData.Get("targetURLPrefixes").Array()
	for _, prefix := range targetURLPrefixes {
		config.targetURLPrefixes = append(config.targetURLPrefixes, prefix.Str)
	}

	if len(config.targetURLPrefixes) < 1 {
		return pluginConfiguration{}, fmt.Errorf("the plugin configuration is missing targetURLPrefixes: %q", string(data))
	}

	var problemTypeURIMap map[string]string
	tempProblemTypeURIMap := jsonData.Get("problemTypeURIMap").Map()
	if len(tempProblemTypeURIMap) == 0 {
		config.problemTypeURIMap = defaultProblemTypeURIMap

	} else {
		for k, v := range tempProblemTypeURIMap {
			problemTypeURIMap[k] = v.String()
		}
		config.problemTypeURIMap = problemTypeURIMap
	}

	problemTitle := jsonData.Get("problemTitle").String()
	if problemTitle == "" {
		problemTitle = defaultProblemTitle
	}
	config.problemTitle = problemTitle

	// If non-sensical input is given for the start/end status code use our own defaults
	startStatusCode := jsonData.Get("startStatusCode").Int()
	if startStatusCode < 400 {
		startStatusCode = 400
	}
	config.startStatusCode = int(startStatusCode)
	endStatusCode := jsonData.Get("endStatusCode").Int()
	if endStatusCode < 400 || endStatusCode > 599 {
		endStatusCode = 599
	}
	config.endStatusCode = int(endStatusCode)

	return *config, nil
}

// Override types.DefaultPluginContext.
func (ctx *pluginContext) NewHttpContext(contextID uint32) types.HttpContext {
	return &customErrorsContext{
		targetURLPrefixes: ctx.configuration.targetURLPrefixes,
		startStatusCode:   ctx.configuration.startStatusCode,
		endStatusCode:     ctx.configuration.endStatusCode,
		problemTypeURIMap: ctx.configuration.problemTypeURIMap,
		problemTitle:      ctx.configuration.problemTitle,
		modifyResponse:    false,
	}
}

// customErrorResponse represents the information that will be included in the problem JSON response
type customErrorResponse struct {
	// type" (string) - A URI reference [RFC3986] that identifies the problem type.
	Type string `json:"type"`
	// "title" (string) - A short, human-readable summary of the problem type
	// in our case this will be hard-coded to a single value for all types of errors
	// we can change this in future if Walmart wants unique values to be used
	Title string `json:"title"`
	// The HTTP status code
	Status int `json:"status"`
	// This is just the request path
	Instance string `json:"instance"`
	// The trace id for the purpose of error correlation, usually the value of the W3C Traceparent header or the istio x-request-id header
	// if neither are present in the request/response then use a static value
	TraceID string `json:"trace_id"`
	// The original error text returned by Istio
	Detail string `json:"detail"`
}

// customErrorsContext implements types.HttpContext interface of proxy-wasm-go SDK.
type customErrorsContext struct {
	// Embed the default root http context here,
	// so that we don't need to reimplement all the methods.
	types.DefaultHttpContext

	// totalResponseBodySize
	totalResponseBodySize int

	// the requestURL - used to determine if we should modify the response or not
	requestURL string
	// the request path e.g. if the ur is `https://foo.com/bar` the path would be `/bar`
	requestPath string
	traceID     string
	statusCode  int

	// modifyResponse when true will result in the response being sent back in rfc9457 format
	modifyResponse bool

	// Only modify responses for specific endpoint prefixes
	targetURLPrefixes []string
	// Only modify response status code >= startStatusCode && <= endStatusCode
	// The plugin will default to startStatusCode = 400 and endStatusCode = 599
	startStatusCode   int
	endStatusCode     int
	problemTypeURIMap map[string]string
	// Defaults to "service mesh returned an error"
	problemTitle string
}

// MatchesTargetURLPrefixes returns true if the request URL matches one of the targetURLPrefixes
func MatchesTargetURLPrefixes(requestURL string, targetURLPrefixes []string) bool {
	for _, prefix := range targetURLPrefixes {
		if strings.Contains(requestURL, prefix) {
			return true
		}
	}
	return false
}

// GetProblemTypeURI returns the problem type URI for a specific status code
func GetProblemTypeURI(statusCode string, problemTypeURIMap map[string]string) string {
	problemTypeURI := ""
	if val, ok := problemTypeURIMap[statusCode]; ok {
		problemTypeURI = val
	}

	if problemTypeURI == "" && strings.HasPrefix(statusCode, "4") {
		problemTypeURI = default4xxProblemTypeURI
	} else if problemTypeURI == "" && strings.HasPrefix(statusCode, "5") {
		problemTypeURI = default5xxProblemTypeURI
	}
	return problemTypeURI
}

// Override types.DefaultHttpContext.
func (ctx *customErrorsContext) OnHttpRequestHeaders(numHeaders int, endOfStream bool) types.Action {

	var requestURL string
	var traceID string

	proxywasm.LogInfof("BEGIN OnHttpRequestHeaders")

	scheme, err := proxywasm.GetHttpRequestHeader(":scheme")
	if err != nil {
		proxywasm.LogErrorf("failed to get request header scheme. Error: %v", err)
	}

	authority, err := proxywasm.GetHttpRequestHeader(":authority")
	if err != nil {
		proxywasm.LogErrorf("failed to get request header authority. Error: %v", err)
	}

	path, err := proxywasm.GetHttpRequestHeader(":path")
	if err != nil {
		proxywasm.LogErrorf("failed to get request header path. Error: %v", err)
	}

	// If the W3C traceparent header is not present use the istio x-request-id instead
	traceID, err = proxywasm.GetHttpRequestHeader("traceparent")
	if err != nil || traceID == "" {
		proxywasm.LogInfof("failed to get request header traceparent, will use x-request-id instead. Error: %v", err)
		traceID, err = proxywasm.GetHttpRequestHeader("x-request-id")
		// If that is also nil then use the default trace id
		if err != nil || traceID == "" {
			proxywasm.LogInfof("failed to get request header x-request-id, will use the default trace id. Error: %v", err)
			traceID = defaultTraceID
		}
	}

	requestURL = fmt.Sprintf("%s://%s%s", scheme, authority, path)

	ctx.requestURL = requestURL
	ctx.requestPath = path
	ctx.traceID = traceID

	proxywasm.LogInfof("request url: %s, trace id: %s", requestURL, traceID)
	proxywasm.LogInfof("END OnHttpRequestHeaders")

	return types.ActionContinue
}

// Override types.DefaultHttpContext.
func (ctx *customErrorsContext) OnHttpResponseHeaders(numHeaders int, endOfStream bool) types.Action {

	proxywasm.LogInfof("BEGIN OnHttpResponseHeaders")
	var statusCode string
	var statusCodeInt int

	statusCode, err := proxywasm.GetHttpResponseHeader(":status")
	if err != nil {
		proxywasm.LogErrorf("failed to get header status. Error: %v", err)
	}
	statusCodeInt, err = strconv.Atoi(statusCode)
	if err != nil {
		proxywasm.LogErrorf("failed to convert status code from string to int. Error: %v", err)
	}
	ctx.statusCode = statusCodeInt
	proxywasm.LogInfof("status code int %v", statusCodeInt)

	contentType, err := proxywasm.GetHttpResponseHeader("content-type")
	if err != nil {
		proxywasm.LogErrorf("failed to get content-type header. Error: %v", err)
	}

	// Only modify the response for the configured status codes AND if the request URL is one that we are intersted in
	if statusCodeInt >= ctx.startStatusCode && statusCodeInt <= ctx.endStatusCode && MatchesTargetURLPrefixes(ctx.requestURL, ctx.targetURLPrefixes) {

		if contentType == "application/problem+json" {
			// The content type is already set correctly so assume the payload is of the right format and do nothing
			return types.ActionContinue
		}

		// Not sure how we can set this from OnHttpResponseBody so lets remove it
		// since the content-length will be different when we replace the body
		if err := proxywasm.RemoveHttpResponseHeader("content-length"); err != nil {
			proxywasm.LogErrorf("failed to remove content length. Error: %v", err)
			//panic(err)
		}

		err = proxywasm.ReplaceHttpResponseHeader("content-type", "application/problem+json")
		if err != nil {
			proxywasm.LogErrorf("failed to set content type to application/json. Error: %v", err)
			return types.ActionContinue
		}
		ctx.modifyResponse = true
		proxywasm.LogInfof("Response eligible for modification to rfc9457 format")
	}

	proxywasm.LogInfof("END OnHttpResponseHeaders")

	return types.ActionContinue
}

// Override types.DefaultHttpContext.
// This does not get called when the status code is 404!
// So this needs to be supplemented with a envoy filter that uses local_reply
func (ctx *customErrorsContext) OnHttpResponseBody(bodySize int, endOfStream bool) types.Action {
	if !ctx.modifyResponse {
		return types.ActionContinue
	}
	proxywasm.LogInfof("BEGIN OnHttpResponseBody")
	ctx.totalResponseBodySize += bodySize
	if !endOfStream {
		// Wait until we see the entire body before modifying it.
		return types.ActionPause
	}

	originalBody, err := proxywasm.GetHttpResponseBody(0, ctx.totalResponseBodySize)
	if err != nil {
		proxywasm.LogErrorf("failed to get response body. Error: %v", err)
		return types.ActionContinue
	}

	problemTypeURI := GetProblemTypeURI(strconv.Itoa(ctx.statusCode), ctx.problemTypeURIMap)

	response := &customErrorResponse{
		Type:     problemTypeURI,
		Title:    ctx.problemTitle,
		Status:   ctx.statusCode,
		TraceID:  ctx.traceID,
		Instance: ctx.requestPath,
		Detail:   string(originalBody),
	}

	b, err := json.Marshal(response)
	if err != nil {
		proxywasm.LogErrorf("failed to marshal response struct to JSON. Error: %v", err)
		return types.ActionContinue
	}

	err = proxywasm.ReplaceHttpResponseBody(b)
	if err != nil {
		proxywasm.LogErrorf("failed to replace response body. Error: %v", err)
		return types.ActionContinue
	}
	proxywasm.LogInfof("Successfully transformed the response to rfc9457 format")
	proxywasm.LogInfof("END OnHttpResponseBody")

	return types.ActionContinue
}
