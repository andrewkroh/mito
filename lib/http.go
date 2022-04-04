// Licensed to Elasticsearch B.V. under one or more contributor
// license agreements. See the NOTICE file distributed with
// this work for additional information regarding copyright
// ownership. Elasticsearch B.V. licenses this file to you under
// the Apache License, Version 2.0 (the "License"); you may
// not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing,
// software distributed under the License is distributed on an
// "AS IS" BASIS, WITHOUT WARRANTIES OR CONDITIONS OF ANY
// KIND, either express or implied.  See the License for the
// specific language governing permissions and limitations
// under the License.

package lib

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"reflect"
	"strings"

	"github.com/google/cel-go/cel"
	"github.com/google/cel-go/checker/decls"
	"github.com/google/cel-go/common/types"
	"github.com/google/cel-go/common/types/ref"
	"github.com/google/cel-go/common/types/traits"
	"github.com/google/cel-go/interpreter/functions"
	"golang.org/x/time/rate"
	expr "google.golang.org/genproto/googleapis/api/expr/v1alpha1"
)

// HTTP returns a cel.EnvOption to configure extended functions for HTTP
// requests. Requests and responses are returned as maps corresponding to
// the Go http.Request and http.Response structs. The client and limit parameters
// will be used for the requests and API rate limiting. If client is nil
// the http.DefaultClient will be used and if limit is nil an non-limiting
// rate.Limiter will be used.
//
// HEAD
//
// head performs a HEAD method request and returns the result:
//
//     head(<string>) -> <map<string,dyn>>
//
// Example:
//
//     head('http://www.example.com/')  // returns {"Body": "", "Close": false,
//
//
// GET
//
// get performs a GET method request and returns the result:
//
//     get(<string>) -> <map<string,dyn>>
//
// Example:
//
//     get('http://www.example.com/')  // returns {"Body": "PCFkb2N0e...
//
//
// GET Request
//
// get returns a GET method request:
//
//     get(<string>) -> <map<string,dyn>>
//
// Example:
//
//     get_request('http://www.example.com/')
//
//     will return:
//
//     {
//         "Close": false,
//         "ContentLength": 0,
//         "Header": {},
//         "Host": "www.example.com",
//         "Method": "GET",
//         "Proto": "HTTP/1.1",
//         "ProtoMajor": 1,
//         "ProtoMinor": 1,
//         "URL": "http://www.example.com/"
//     }
//
//
// POST
//
// post performs a POST method request and returns the result:
//
//     post(<string>, <string>, <bytes>) -> <map<string,dyn>>
//     post(<string>, <string>, <string>) -> <map<string,dyn>>
//
// Example:
//
//     post("http://www.example.com/", "text/plain", "test")  // returns {"Body": "PCFkb2N0e...
//
//
// POST Request
//
// post_request returns a POST method request:
//
//     post_request(<string>, <string>, <bytes>) -> <map<string,dyn>>
//     post_request(<string>, <string>, <string>) -> <map<string,dyn>>
//
// Example:
//
//     post("http://www.example.com/", "text/plain", "test")
//
//     will return:
//
//     {
//         "Body": "test",
//         "Close": false,
//         "ContentLength": 4,
//         "Header": {
//             "Content-Type": [
//                 "text/plain"
//             ]
//         },
//         "Host": "www.example.com",
//         "Method": "POST",
//         "Proto": "HTTP/1.1",
//         "ProtoMajor": 1,
//         "ProtoMinor": 1,
//         "URL": "http://www.example.com/"
//     }
//
//
// Request
//
// request returns a user-defined method request:
//
//     request(<string>, <string>, <string>, <bytes>) -> <map<string,dyn>>
//     request(<string>, <string>, <string>, <string>) -> <map<string,dyn>>
//
// Example:
//
//     request("GET", "http://www.example.com/").with({"header":{
//         "Authorization": "Basic "+string(base64("username:password")),
//     }})
//
//     will return:
//
//     {
//         "Close": false,
//         "ContentLength": 0,
//         "Header": {},
//         "Host": "www.example.com",
//         "Method": "GET",
//         "Proto": "HTTP/1.1",
//         "ProtoMajor": 1,
//         "ProtoMinor": 1,
//         "URL": "http://www.example.com/",
//         "header": {
//             "Authorization": "Basic dXNlcm5hbWU6cGFzc3dvcmQ="
//         }
//     },
//
//
// Do Request
//
// do_request executes an HTTP request:
//
//     <map<string,dyn>>.do_request() -> <map<string,dyn>>
//
// Example:
//
//     get_request("http://www.example.com/").do_request()  // returns {"Body": "PCFkb2N0e...
//
func HTTP(client *http.Client, limit *rate.Limiter) cel.EnvOption {
	if client == nil {
		client = http.DefaultClient
	}
	if limit == nil {
		limit = rate.NewLimiter(rate.Inf, 0)
	}
	return cel.Lib(httpLib{
		client: client,
		limit:  limit,
	})
}

type httpLib struct {
	client *http.Client
	limit  *rate.Limiter
}

func (httpLib) CompileOptions() []cel.EnvOption {
	return []cel.EnvOption{
		cel.Declarations(
			decls.NewFunction("head",
				decls.NewOverload(
					"head_string",
					[]*expr.Type{decls.String},
					decls.NewMapType(decls.String, decls.Dyn),
				),
			),
			decls.NewFunction("get",
				decls.NewOverload(
					"get_string",
					[]*expr.Type{decls.String},
					decls.NewMapType(decls.String, decls.Dyn),
				),
			),
			decls.NewFunction("get_request",
				decls.NewOverload(
					"get_request_string",
					[]*expr.Type{decls.String},
					decls.NewMapType(decls.String, decls.Dyn),
				),
			),
			decls.NewFunction("post",
				decls.NewOverload(
					"post_string_string_bytes",
					[]*expr.Type{decls.String, decls.String, decls.Bytes},
					decls.NewMapType(decls.String, decls.Dyn),
				),
				decls.NewOverload(
					"post_string_string_string",
					[]*expr.Type{decls.String, decls.String, decls.String},
					decls.NewMapType(decls.String, decls.Dyn),
				),
			),
			decls.NewFunction("post_request",
				decls.NewOverload(
					"post_request_string_string_bytes",
					[]*expr.Type{decls.String, decls.String, decls.Bytes},
					decls.NewMapType(decls.String, decls.Dyn),
				),
				decls.NewOverload(
					"post_request_string_string_string",
					[]*expr.Type{decls.String, decls.String, decls.String},
					decls.NewMapType(decls.String, decls.Dyn),
				),
			),
			decls.NewFunction("request",
				decls.NewOverload(
					"request_string_string",
					[]*expr.Type{decls.String, decls.String},
					decls.NewMapType(decls.String, decls.Dyn),
				),
				decls.NewOverload(
					"request_string_string_bytes",
					[]*expr.Type{decls.String, decls.String, decls.Bytes},
					decls.NewMapType(decls.String, decls.Dyn),
				),
				decls.NewOverload(
					"request_string_string_string",
					[]*expr.Type{decls.String, decls.String, decls.String},
					decls.NewMapType(decls.String, decls.Dyn),
				),
			),
			decls.NewFunction("do_request",
				decls.NewInstanceOverload(
					"map_do_request",
					[]*expr.Type{decls.NewMapType(decls.String, decls.Dyn)},
					decls.NewMapType(decls.String, decls.Dyn),
				),
			),
		),
	}
}

func (l httpLib) ProgramOptions() []cel.ProgramOption {
	return []cel.ProgramOption{
		cel.Functions(
			&functions.Overload{
				Operator: "head_string",
				Unary:    l.doHead,
			},
		),
		cel.Functions(
			&functions.Overload{
				Operator: "get_string",
				Unary:    l.doGet,
			},
		),
		cel.Functions(
			&functions.Overload{
				Operator: "get_request_string",
				Unary:    newGetRequest,
			},
		),
		cel.Functions(
			&functions.Overload{
				Operator: "post_string_string_bytes",
				Function: l.doPost,
			},
			&functions.Overload{
				Operator: "post_string_string_string",
				Function: l.doPost,
			},
		),
		cel.Functions(
			&functions.Overload{
				Operator: "post_request_string_string_bytes",
				Function: newPostRequest,
			},
			&functions.Overload{
				Operator: "post_request_string_string_string",
				Function: newPostRequest,
			},
		),
		cel.Functions(
			&functions.Overload{
				Operator: "request_string_string",
				Binary:   newRequest,
			},
			&functions.Overload{
				Operator: "request_string_string_bytes",
				Function: newRequestBody,
			},
			&functions.Overload{
				Operator: "request_string_string_string",
				Function: newRequestBody,
			},
		),
		cel.Functions(
			&functions.Overload{
				Operator: "map_do_request",
				Unary:    l.doRequest,
			},
		),
	}
}

func (l httpLib) doHead(arg ref.Val) ref.Val {
	url, ok := arg.(types.String)
	if !ok {
		return types.ValOrErr(url, "no such overload for head")
	}
	err := l.limit.Wait(context.TODO())
	if err != nil {
		return types.NewErr("%s", err)
	}
	resp, err := l.client.Head(string(url))
	if err != nil {
		return types.NewErr("%s", err)
	}
	rm, err := respToMap(resp)
	if err != nil {
		return types.NewErr("%s", err)
	}
	return types.DefaultTypeAdapter.NativeToValue(rm)
}

func (l httpLib) doGet(arg ref.Val) ref.Val {
	url, ok := arg.(types.String)
	if !ok {
		return types.ValOrErr(url, "no such overload for get")
	}
	err := l.limit.Wait(context.TODO())
	if err != nil {
		return types.NewErr("%s", err)
	}
	resp, err := l.client.Get(string(url))
	if err != nil {
		return types.NewErr("%s", err)
	}
	rm, err := respToMap(resp)
	if err != nil {
		return types.NewErr("%s", err)
	}
	return types.DefaultTypeAdapter.NativeToValue(rm)
}

func newGetRequest(url ref.Val) ref.Val {
	return newRequestBody(types.String("GET"), url)
}

func (l httpLib) doPost(args ...ref.Val) ref.Val {
	if len(args) != 3 {
		return types.NewErr("no such overload for post")
	}
	url, ok := args[0].(types.String)
	if !ok {
		return types.ValOrErr(url, "no such overload for request")
	}
	content, ok := args[1].(types.String)
	if !ok {
		return types.ValOrErr(content, "no such overload for request")
	}
	var body io.Reader
	switch text := args[2].(type) {
	case types.Bytes:
		if len(text) != 0 {
			body = bytes.NewReader(text)
		}
	case types.String:
		if text != "" {
			body = strings.NewReader(string(text))
		}
	default:
		return types.NewErr("invalid type for post body: %s", text.Type())
	}
	err := l.limit.Wait(context.TODO())
	if err != nil {
		return types.NewErr("%s", err)
	}
	resp, err := l.client.Post(string(url), string(content), body)
	if err != nil {
		return types.NewErr("%s", err)
	}
	rm, err := respToMap(resp)
	if err != nil {
		return types.NewErr("%s", err)
	}
	return types.DefaultTypeAdapter.NativeToValue(rm)
}

func newPostRequest(args ...ref.Val) ref.Val {
	if len(args) != 3 {
		return types.NewErr("no such overload for post request")
	}
	content, ok := args[1].(types.String)
	if !ok {
		return types.ValOrErr(content, "no such overload for request")
	}
	url := args[0]
	body := args[2]
	req, err := makeRequestBody(types.String("POST"), url, body)
	if err != nil {
		return err
	}
	h, ok := req["Header"]
	if !ok {
		h = make(http.Header)
		req["Header"] = h
	}
	h.(http.Header).Set("Content-Type", string(content))
	return types.DefaultTypeAdapter.NativeToValue(req)
}

func newRequest(method, url ref.Val) ref.Val {
	return newRequestBody(method, url)
}

func newRequestBody(args ...ref.Val) ref.Val {
	req, err := makeRequestBody(args...)
	if err != nil {
		return err
	}
	return types.DefaultTypeAdapter.NativeToValue(req)
}

func makeRequestBody(args ...ref.Val) (map[string]interface{}, ref.Val) {
	if len(args) < 2 {
		return nil, types.NewErr("no such overload for request")
	}
	method, ok := args[0].(types.String)
	if !ok {
		return nil, types.ValOrErr(method, "no such overload for request")
	}
	url, ok := args[1].(types.String)
	if !ok {
		return nil, types.ValOrErr(method, "no such overload for request")
	}
	var (
		body       ref.Val
		bodyReader io.Reader
	)
	if len(args) == 3 {
		body = args[2]
		switch body := body.(type) {
		case types.Bytes:
			if len(body) != 0 {
				bodyReader = bytes.NewReader(body)
			}
		case types.String:
			if body != "" {
				bodyReader = strings.NewReader(string(body))
			}
		default:
			return nil, types.NewErr("invalid type for request body: %s", body.Type())
		}
	}
	req, err := http.NewRequest(string(method), string(url), bodyReader)
	if err != nil {
		return nil, types.NewErr("%s", err)
	}
	reqMap, err := reqToMap(req, url, body)
	if err != nil {
		return nil, types.NewErr("%s", err)
	}
	return reqMap, nil
}

func reqToMap(req *http.Request, url, body ref.Val) (map[string]interface{}, error) {
	rm := map[string]interface{}{
		"Method":        req.Method,
		"URL":           url,
		"Proto":         req.Proto,
		"ProtoMajor":    req.ProtoMajor,
		"ProtoMinor":    req.ProtoMinor,
		"Header":        req.Header,
		"ContentLength": req.ContentLength,
		"Close":         req.Close,
		"Host":          req.Host,
	}
	if req.RequestURI != "" {
		rm["RequestURI"] = req.RequestURI
	}
	if body != nil {
		rm["Body"] = body
	}
	if req.TransferEncoding != nil {
		rm["TransferEncoding"] = req.TransferEncoding
	}
	if req.Trailer != nil {
		rm["Trailer"] = req.Trailer
	}
	if req.Response != nil {
		resp, err := respToMap(req.Response)
		if err != nil {
			return nil, err
		}
		rm["Response"] = resp
	}
	return rm, nil
}

func respToMap(resp *http.Response) (map[string]interface{}, error) {
	rm := map[string]interface{}{
		"Status":        resp.Status,
		"StatusCode":    resp.StatusCode,
		"Proto":         resp.Proto,
		"ProtoMajor":    resp.ProtoMajor,
		"ProtoMinor":    resp.ProtoMinor,
		"Header":        resp.Header,
		"ContentLength": resp.ContentLength,
		"Close":         resp.Close,
		"Uncompressed":  resp.Uncompressed,
	}
	var buf bytes.Buffer
	_, err := io.Copy(&buf, resp.Body)
	resp.Body.Close()
	if err != nil {
		return nil, err
	}
	rm["Body"] = buf.Bytes()
	if resp.TransferEncoding != nil {
		rm["TransferEncoding"] = resp.TransferEncoding
	}
	if resp.Trailer != nil {
		rm["Trailer"] = resp.Trailer
	}
	if resp.Request != nil {
		req, err := reqToMap(resp.Request, types.String(resp.Request.URL.String()), nil)
		if err != nil {
			return nil, err
		}
		rm["Request"] = req
	}
	return rm, nil
}

func (l httpLib) doRequest(arg ref.Val) ref.Val {
	request, ok := arg.(traits.Mapper)
	if !ok {
		return types.ValOrErr(request, "no such overload for do_request")
	}
	reqm, err := request.ConvertToNative(reflectMapStringAnyType)
	if err != nil {
		return types.NewErr("%s", err)
	}
	req, err := mapToReq(reqm.(map[string]interface{}))
	if err != nil {
		return types.NewErr("%s", err)
	}
	// Recover the context lost during serialisation to JSON.
	req = req.WithContext(context.Background())
	err = l.limit.Wait(context.TODO())
	if err != nil {
		return types.NewErr("%s", err)
	}
	resp, err := l.client.Do(req)
	if err != nil {
		return types.NewErr("%s", err)
	}
	respm, err := respToMap(resp)
	if err != nil {
		return types.NewErr("%s", err)
	}
	return types.DefaultTypeAdapter.NativeToValue(respm)
}

func mapToReq(rm map[string]interface{}) (*http.Request, error) {
	if rm == nil {
		return nil, nil
	}
	req := &http.Request{}
	err := mapConv(reflect.ValueOf(req).Elem(), rm)
	return req, err
}

func mapToResp(rm map[string]interface{}) (*http.Response, error) {
	if rm == nil {
		return nil, nil
	}
	resp := &http.Response{}
	err := mapConv(reflect.ValueOf(resp).Elem(), rm)
	return resp, err
}

func mapConv(dst reflect.Value, src map[string]interface{}) error {
	rt := dst.Type()
	for i := 0; i < dst.NumField(); i++ {
		ft := rt.Field(i)
		if !ft.IsExported() {
			continue
		}
		v, ok := src[ft.Name]
		if !ok {
			continue
		}
		conv, ok := convFuncs[ft.Type.String()]
		if !ok {
			continue
		}
		val, err := conv(reflect.ValueOf(v))
		if err != nil {
			return err
		}
		dst.Field(i).Set(val)
	}
	return nil
}

var convFuncs = map[string]func(val reflect.Value) (reflect.Value, error){
	"int":                  func(val reflect.Value) (reflect.Value, error) { return val.Convert(reflectIntType), nil },
	"int64":                func(val reflect.Value) (reflect.Value, error) { return val.Convert(reflectInt64Type), nil },
	"bool":                 func(val reflect.Value) (reflect.Value, error) { return val.Convert(reflectBoolType), nil },
	"string":               func(val reflect.Value) (reflect.Value, error) { return val.Convert(reflectStringType), nil },
	"[]string":             makeStrings,
	"io.ReadCloser":        makeBody,
	"*url.URL":             makeURL,
	"http.Header":          makeMapStrings,
	"url.Values":           makeMapStrings,
	"*multipart.Form":      func(val reflect.Value) (reflect.Value, error) { panic("TODO") },
	"*tls.ConnectionState": func(val reflect.Value) (reflect.Value, error) { panic("TODO") },

	// These should pass through without this being implemented, but mark them.
	"*http.Request":  func(val reflect.Value) (reflect.Value, error) { panic("REPORT BUG: http.Request") },
	"*http.Response": func(val reflect.Value) (reflect.Value, error) { panic("REPORT BUG: http.Response") },
}

func makeMapStrings(val reflect.Value) (reflect.Value, error) {
	iface := val.Interface()
	switch iface := iface.(type) {
	case http.Header:
		return reflect.ValueOf(iface), nil
	case url.Values:
		return reflect.ValueOf(iface), nil
	case map[string][]string:
		return reflect.ValueOf(iface), nil
	case map[ref.Val]ref.Val:
		val := types.DefaultTypeAdapter.NativeToValue(iface)
		v, err := val.ConvertToNative(reflectMapStringStringSliceType)
		if err != nil {
			return reflect.Value{}, err
		}
		return reflect.ValueOf(v), nil
	case ref.Val:
		v, err := iface.ConvertToNative(reflectMapStringStringSliceType)
		if err != nil {
			return reflect.Value{}, err
		}
		return reflect.ValueOf(v.(map[string][]string)), nil
	default:
		return reflect.Value{}, fmt.Errorf("invalid type: %T", iface)
	}
}

func makeStrings(val reflect.Value) (reflect.Value, error) {
	iface := val.Interface()
	switch iface := iface.(type) {
	case []string:
		return reflect.ValueOf(iface), nil
	case []types.String:
		dst := make([]string, len(iface))
		for i, s := range iface {
			dst[i] = string(s)
		}
		return reflect.ValueOf(dst), nil
	case ref.Val:
		v, err := iface.ConvertToNative(reflectStringSliceType)
		if err != nil {
			return reflect.Value{}, err
		}
		return reflect.ValueOf(v), nil
	case []ref.Val:
		dst := make([]string, len(iface))
		for i, s := range iface {
			v, err := s.ConvertToNative(reflectStringType)
			if err != nil {
				return reflect.Value{}, err
			}
			dst[i] = v.(string)
		}
		return reflect.ValueOf(dst), nil
	default:
		return reflect.Value{}, fmt.Errorf("invalid type: %T", iface)
	}
}

func makeBody(val reflect.Value) (reflect.Value, error) {
	var r io.Reader
	switch val.Kind() {
	case reflect.String:
		r = strings.NewReader(val.String())
	case reflect.Slice:
		if !val.CanConvert(reflectByteSliceType) {
			return reflect.Value{}, fmt.Errorf("invalid type: %s", val.Type())
		}
		r = bytes.NewReader(val.Bytes())
	default:
		return reflect.Value{}, fmt.Errorf("invalid type: %s", val.Type())
	}
	return reflect.ValueOf(io.NopCloser(r)), nil
}

func makeURL(val reflect.Value) (reflect.Value, error) {
	if val.Kind() != reflect.String {
		return reflect.Value{}, fmt.Errorf("invalid type: %s", val.Type())
	}
	u, err := url.Parse(val.String())
	if err != nil {
		return reflect.Value{}, err
	}
	return reflect.ValueOf(u), nil
}