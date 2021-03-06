// Copyright (C) 2017  Arista Networks, Inc.
// Use of this source code is governed by the Apache License 2.0
// that can be found in the COPYING file.

package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/ioutil"
	"os"

	pb "github.com/openconfig/gnmi/proto/gnmi"
	"google.golang.org/grpc/codes"

	"github.com/aristanetworks/glog"
	"github.com/aristanetworks/goarista/gnmi"
)

// TODO: Make this more clear
var help = `Usage of gnmi:
gnmi [options]
  capabilities
  get PATH+
  subscribe PATH+
  ((update|replace PATH JSON)|(delete PATH))+
`

func exitWithError(s string) {
	flag.Usage()
	fmt.Fprintln(os.Stderr, s)
	os.Exit(1)
}

type operation struct {
	opType string
	path   []string
	val    string
}

func main() {
	cfg := &gnmi.Config{}
	flag.StringVar(&cfg.Addr, "addr", "", "Address of gNMI gRPC server")
	flag.StringVar(&cfg.CAFile, "cafile", "", "Path to server TLS certificate file")
	flag.StringVar(&cfg.CertFile, "certfile", "", "Path to client TLS certificate file")
	flag.StringVar(&cfg.KeyFile, "keyfile", "", "Path to client TLS private key file")
	flag.StringVar(&cfg.Password, "password", "", "Password to authenticate with")
	flag.StringVar(&cfg.Username, "username", "", "Username to authenticate with")
	flag.BoolVar(&cfg.TLS, "tls", false, "Enable TLS")

	flag.Usage = func() {
		fmt.Fprintln(os.Stderr, help)
		flag.PrintDefaults()
	}
	flag.Parse()
	args := flag.Args()

	ctx := gnmi.NewContext(context.Background(), cfg)
	client := gnmi.Dial(cfg)

	var setOps []*operation
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "capabilities":
			if len(setOps) != 0 {
				exitWithError("error: 'capabilities' not allowed after 'merge|replace|delete'")
			}
			exitWithError("error: 'capabilities' not supported")
			return
		case "get":
			if len(setOps) != 0 {
				exitWithError("error: 'get' not allowed after 'merge|replace|delete'")
			}
			err := get(ctx, client, gnmi.SplitPaths(args[i+1:]))
			if err != nil {
				glog.Fatal(err)
			}
			return
		case "subscribe":
			if len(setOps) != 0 {
				exitWithError("error: 'subscribe' not allowed after 'merge|replace|delete'")
			}
			err := subscribe(ctx, client, gnmi.SplitPaths(args[i+1:]))
			if err != nil {
				glog.Fatal(err)
			}
			return
		case "update", "replace", "delete":
			if len(args) == i+1 {
				exitWithError("error: missing path")
			}
			op := &operation{
				opType: args[i],
			}
			i++
			op.path = gnmi.SplitPath(args[i])
			if op.opType != "delete" {
				if len(args) == i+1 {
					exitWithError("error: missing JSON")
				}
				i++
				op.val = args[i]
			}
			setOps = append(setOps, op)
		default:
			exitWithError(fmt.Sprintf("error: unknown operation %q", args[i]))
		}
	}
	if len(setOps) == 0 {
		flag.Usage()
		os.Exit(1)
	}
	err := set(ctx, client, setOps)
	if err != nil {
		glog.Fatal(err)
	}

}

func get(ctx context.Context, client pb.GNMIClient, paths [][]string) error {
	req, err := gnmi.NewGetRequest(paths)
	if err != nil {
		return err
	}
	resp, err := client.Get(ctx, req)
	if err != nil {
		return err
	}
	for _, notif := range resp.Notification {
		for _, update := range notif.Update {
			fmt.Printf("%s:\n", gnmi.StrPath(update.Path))
			fmt.Println(strVal(update))
		}
	}
	return nil
}

// val may be a path to a file or it may be json. First see if it is a
// file, if so return its contents, otherwise return val
func extractJSON(val string) []byte {
	jsonBytes, err := ioutil.ReadFile(val)
	if err != nil {
		jsonBytes = []byte(val)
	}
	return jsonBytes
}

// strVal will return a string representing the value within the supplied update
func strVal(u *pb.Update) string {
	if u.Value != nil {
		return string(u.Value.Value) // Backwards compatibility with pre-v0.4 gnmi
	}

	switch v := u.Val.GetValue().(type) {
	case *pb.TypedValue_StringVal:
		return v.StringVal
	case *pb.TypedValue_JsonIetfVal:
		return string(v.JsonIetfVal)
	case *pb.TypedValue_IntVal:
		return fmt.Sprintf("%v", v.IntVal)
	case *pb.TypedValue_UintVal:
		return fmt.Sprintf("%v", v.UintVal)
	case *pb.TypedValue_BoolVal:
		return fmt.Sprintf("%v", v.BoolVal)
	case *pb.TypedValue_BytesVal:
		return string(v.BytesVal)
	case *pb.TypedValue_DecimalVal:
		return strDecimal64(v.DecimalVal)
	default:
		return fmt.Sprintf("[oops - %T]", v)
	}
}

func strDecimal64(d *pb.Decimal64) string {
	var i, frac uint64
	if d.Precision > 0 {
		div := uint64(10)
		it := d.Precision - 1
		for it > 0 {
			div *= 10
			it--
		}
		i = d.Digits / div
		frac = d.Digits % div
	} else {
		i = d.Digits
	}
	return fmt.Sprintf("%d.%d", i, frac)
}

func update(p *pb.Path, v []byte) *pb.Update {
	return &pb.Update{Path: p, Val: jsonval(v)}
}

func jsonval(j []byte) *pb.TypedValue {
	return &pb.TypedValue{Value: &pb.TypedValue_JsonIetfVal{JsonIetfVal: j}}
}

func set(ctx context.Context, client pb.GNMIClient, setOps []*operation) error {
	req := &pb.SetRequest{}
	for _, op := range setOps {
		elm, err := gnmi.ParseGNMIElements(op.path)
		if err != nil {
			return err
		}
		p := &pb.Path{
			Element: op.path, // Backwards compatibility with pre-v0.4 gnmi
			Elem:    elm,
		}

		switch op.opType {
		case "delete":
			req.Delete = append(req.Delete, p)
		case "update":
			req.Update = append(req.Update, update(p, extractJSON(op.val)))
		case "replace":
			req.Replace = append(req.Replace, update(p, extractJSON(op.val)))
		}
	}

	resp, err := client.Set(ctx, req)
	if err != nil {
		return err
	}
	if resp.Message != nil && codes.Code(resp.Message.Code) != codes.OK {
		return errors.New(resp.Message.Message)
	}
	// TODO: Iterate over SetResponse.Response for more detailed error message?

	return nil
}

func subscribe(ctx context.Context, client pb.GNMIClient, paths [][]string) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	stream, err := client.Subscribe(ctx)
	if err != nil {
		return err
	}
	req, err := gnmi.NewSubscribeRequest(paths)
	if err != nil {
		return err
	}
	if err := stream.Send(req); err != nil {
		return err
	}

	for {
		response, err := stream.Recv()
		if err != nil {
			if err == io.EOF {
				return nil
			}
			return err
		}
		switch resp := response.Response.(type) {
		case *pb.SubscribeResponse_Error:
			return errors.New(resp.Error.Message)
		case *pb.SubscribeResponse_SyncResponse:
			if !resp.SyncResponse {
				return errors.New("initial sync failed")
			}
		case *pb.SubscribeResponse_Update:
			for _, update := range resp.Update.Update {
				fmt.Printf("%s = %s\n", gnmi.StrPath(update.Path),
					strVal(update))
			}
		}
	}
}
