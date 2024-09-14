# gRPC Web Go client
[![GoDoc](https://godoc.org/github.com/heartandu/grpc-web-go-client/grpcweb?status.svg)](https://godoc.org/github.com/heartandu/grpc-web-go-client/grpcweb)

*THE IMPLEMENTATION IS LACKING*

gRPC Web client written in Go.

## Usage
Send an unary request.

``` go
client := grpcweb.NewClient("localhost:50051")

in, out := new(api.SimpleRequest), new(api.SimpleResponse)
in.Name = "ktr"

if err := client.Invoke(context.Background(), "/api.Example/Unary", in, out); err != nil {
    log.Fatal(err)
}

// hello, ktr
fmt.Println(out.GetMessage())
```

Send a server-side streaming request.

``` go
streamDesc := &grps.StreamDesc{
    StreamName:    "ServerStreaming",
    ServerStreams: true,
}

stream, err := client.NewStream(context.Background(), streamDesc, "/api.Example/ServerStreaming")
if err != nil {
    log.Fatal(err)
}

in := new(api.SimpleRequest)
in.Name = "ktr"

if err := stream.SendMsg(in); err != nil {
    log.Fatalf(err)
}

if err := stream.CloseSend(); err != nil {
    log.Fatalf(err)
}

for {
    res := new(api.SimpleResponse)
    if err := stream.RecvMsg(res); err != nil {
        if errors.Is(err, io.EOF) {
            break
        }

        log.Fatalf(err)
    }

    log.Println(res.GetMessage())
}
```

Send a client-side streaming request.
``` go
streamDesc := &gprc.StreamDesc{
    StreamName:    "ClientStreaming",
    ClientStreams: true,
}

stream, err := client.NewStream(context.Background(), streamDesc, "/api.Example/ClientStreaming")
if err != nil {
    log.Fatal(err)
}

in, out := new(api.SimpleRequest), new(api.SimpleResponse)
in.Name = "ktr"

for i := 0; i < 10; i++ {
    if err := stream.SendMsg(in); err != nil {
        log.Fatal(err)
    }
}

if err := stream.CloseSend(); err != nil {
    log.Fatal(err)
}

if err := stream.RecvMsg(out); err != nil {
    log.Fatal(err)
}

// ktr, you greet 10 times.
fmt.Println(out.GetMessage())
```

Send a bidirectional streaming request.
``` go
streamDesc := &grpc.StreamDesc{
    StreamName:      "BidiStreaming",
    ClientStreaming: true,
    ServerStreaming: true,
}

stream, err := client.NewStream(context.Background(), streamDesc, "/api.Example/BidiStreaming")
if err != nil {
    log.Fatal(err)
}

go func() {
    for {
        out := new(api.SimpleResponse)

        if err := stream.RecvMsg(out); err != nil {
            if errors.Is(err, io.EOF) {
                return
            }

            log.Fatal(err)
        }

        fmt.Println(res.GetMessage())
    }
}()

for i := 0; i < 2; i++ {
    in := new(api.SimpleRequest)
    in.Name = fmt.Sprintf("ktr%d", i+1)

    if err := stream.SendMsg(in); err != nil {
        if err == io.EOF {
            break
        }

        log.Fatal(err)
    }
}

// wait a moment to get responses.
time.Sleep(10 * time.Second)
```
