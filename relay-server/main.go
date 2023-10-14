package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os/exec"
	"sync"
	"time"
)

var currConn net.Conn
var currHttpServer *http.Server

func handleCamConnect(conn net.Conn, transcode bool, mjpegAddr, mjpegPath, flvAddr, rtmpAddr string) {
	defer func() {
		conn.Close()
		currConn = nil
	}()

	if currConn != nil {
		currConn.Close()
		currConn = nil
	}

	currConn = conn

	if currHttpServer != nil {
		currHttpServer.Shutdown(context.TODO())
		currHttpServer = nil
	}

	log.Printf("mjpeg address: %s%s\n", mjpegAddr, mjpegPath)

	wg := sync.WaitGroup{}
	wg.Add(1)

	consumed := false
	mux := http.NewServeMux()
	mux.HandleFunc(mjpegPath, func(w http.ResponseWriter, r *http.Request) {
		if consumed {
			w.Write([]byte("please try again later"))
			return
		}
		consumed = true

		w.Header().Add("Content-Type", "multipart/x-mixed-replace;boundary=123456789000000000000987654321")
		w.Header().Set("Connection", "Keep-Alive")
		w.Header().Set("X-Content-Type-Options", "nosniff")

		io.Copy(w, conn)

		conn.Close()
		wg.Done()
	})

	srv := &http.Server{Addr: mjpegAddr, Handler: mux}
	go srv.ListenAndServe()
	defer func() {
		srv.Shutdown(context.TODO())
		currHttpServer = nil
	}()

	currHttpServer = srv

	if transcode {
		time.Sleep(time.Millisecond * 500)

		cmdLivego := exec.Command("./livego", "--api_addr", "127.0.0.1:43585", "--hls_addr", "127.0.0.1:43586", "--httpflv_addr", flvAddr, "--rtmp_addr", rtmpAddr)
		if err := cmdLivego.Start(); err == nil {
			defer cmdLivego.Process.Kill()
			go cmdLivego.Wait()
		} else {
			fmt.Println(err)
		}

		time.Sleep(time.Microsecond * 500)
		http.Get("http://127.0.0.1:43585/control/get?room=movie")

		cmdFfmpeg := exec.Command("./ffmpeg", "-use_wallclock_as_timestamps", "1", "-i", fmt.Sprintf("http://127.0.0.1%s%s", mjpegAddr, mjpegPath), "-f", "lavfi", "-i", "anullsrc", "-c:v", "libx264", "-vf", "format=yuv420p", "-crf", "30", "-maxrate", "800k", "-g", "30", "-fflags", "nobuffer", "-c:a", "aac", "-b:a", "1k", "-f", "flv", fmt.Sprintf("rtmp://%s/live/rfBd56ti2SMtYvSgD5xAV0YU99zampta7Z7S575KLkIZ9PYk", rtmpAddr))
		if err := cmdFfmpeg.Start(); err == nil {
			defer cmdFfmpeg.Process.Kill()
			go cmdFfmpeg.Wait()
		} else {
			fmt.Println(err)
		}
		log.Printf("flv stream available at %s/live/movie.flv\n", flvAddr)
	}

	wg.Wait()

	log.Println("closing", conn.RemoteAddr().String())
}

func main() {
	transcode := flag.Bool("transcode", false, "transcode mjpeg and serve flv stream")
	camAddr := flag.String("cam-addr", ":40001", "camera connect address")
	mjpegAddr := flag.String("mjpeg-addr", ":40002", "mjpeg address")
	mjpegPath := flag.String("mjpeg-path", "/cam", "mjpeg path")
	flvAddr := flag.String("flv-addr", ":40003", "transcoded flv stream address")
	rtmpAddr := flag.String("rtmp-addr", "127.0.0.1:40004", "rtmp address for internal usage")
	flag.Parse()

	listener, err := net.Listen("tcp", *camAddr)
	if err != nil {
		log.Fatal(err)
	}
	defer listener.Close()

	log.Println("waiting for camera connection on", *camAddr)

	for {
		conn, err := listener.Accept()
		if err != nil {
			log.Fatal(err)
		}
		log.Println("new camera connection", conn.RemoteAddr().String())

		go handleCamConnect(conn, *transcode, *mjpegAddr, *mjpegPath, *flvAddr, *rtmpAddr)
	}
}
