// Copyright 2021-2026 The N42 Authors
// This file is part of the N42 library.

package evmsdk

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/gorilla/websocket"

	"github.com/n42blockchain/N42/crypto/bls"
)

func Test() string {
	var sw strings.Builder

	sw.WriteString("version:" + VERSION + "\r\n")

	engJsonBytes, err := json.Marshal(EE)
	if err == nil {
		sw.WriteString(string(engJsonBytes) + "\r\n")
	}

	sw.WriteString("gettime:" + strconv.Itoa(int(GetTime())) + "\r\n")
	sw.WriteString("getapppath:" + GetAppPath() + "\r\n")
	sw.WriteString("getjson:" + GetJson() + "\r\n")

	sw.WriteString("readfile_before:" + ReadTouchedFile() + "\r\n")
	sw.WriteString("touchfile:" + TouchFile() + "\r\n")
	sw.WriteString("readfile_after:" + ReadTouchedFile() + "\r\n")

	ns := GetNetInfos()
	sw.WriteString("getnetinfos:resplen:" + strconv.Itoa(len(ns)) + "\r\n")

	sw.WriteString("backgroundthread:" + BackgroundLoop() + "\r\n")
	sw.WriteString("bls tests " + BlsTest() + "\r\n")

	//block here
	sw.WriteString("wsocket:" + GetWebSocketConnect() + "\r\n")

	return sw.String()
}

func GetTime() int64 {
	return time.Now().Unix()
}

func GetAppPath() string {
	p, err := os.Getwd()
	if err != nil {
		return err.Error()
	}
	return fmt.Sprintf("exec path:%+v;;setting_path:%+v", p, EE.AppBasePath)
}

func GetJson() string {
	return `{
		"a":"b",
		"c":"d"
	}`
}

func TouchFile() string {
	filePath := path.Join(EE.AppBasePath, "evm_touched_file.txt")
	file, err := os.Create(filePath)
	if err != nil {
		return err.Error()
	}
	defer file.Close()
	t := time.Now().Unix()
	n, err := file.WriteString(strconv.Itoa(int(t)) + "|NAME=wux PWD=$WORK/src GOOS=android GOARCH=386 CC=$ANDROID_HOME/ndk/23.1.7779620/toolchains/llvm/prebuilt/linux-x86_64/bin/i686-linux-android21-clang CXX=$ANDROID_HOME/ndk/23.1.7779620/toolchains/llvm/prebuilt/linux-x86_64/bin/i686-linux-android21-clang++ CGO_ENABLED=1 GOPATH=$WORK:$GOPATH go mod tidy")
	if err != nil {
		return err.Error()
	}
	if n == 0 {
		return "n==0"
	}
	return file.Name()
}

func ReadTouchedFile() string {
	filePath := path.Join(EE.AppBasePath, "evm_touched_file.txt")
	file, err := os.Open(filePath)
	if err != nil {
		return fmt.Sprintln("open touched file error,err=", err.Error())
	}
	defer file.Close()
	allBytes, err := io.ReadAll(file)
	if err != nil {
		return fmt.Sprintln("read touched file error,err=", err.Error())
	}
	return string(allBytes)
}

func GetNetInfos() string {
	resp, err := http.DefaultClient.Get("https://www.baidu.com")
	if err != nil {
		return err.Error()
	}
	defer resp.Body.Close()
	htmlBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return err.Error()
	}
	return string(htmlBytes)
}

func RunLoop() string {
	for {
		<-time.After(2 * time.Second)
		fmt.Println("1")
	}
}

var backgroundLoopState = ""

func BackgroundLoop() string {
	if len(backgroundLoopState) != 0 {
		fmt.Println("BackgroundLoop running already")
		return "BackgroundLoop running"
	}
	backgroundLoopState = "123"
	go func() {
		for {
			<-time.After(2 * time.Second)
			fmt.Println("alive")
		}
	}()
	return "OK"
}

func GetWebSocketConnect() string {
	var sw strings.Builder
	wsServerURL := os.Getenv("WS_SERVER_URL")
	if wsServerURL == "" {
		wsServerURL = "ws://54.175.247.94:20013"
	}
	conn, connResp, err := websocket.DefaultDialer.Dial(wsServerURL, nil)
	if err != nil {
		return fmt.Sprintf("dial error,err=%+v \r\n", err)
	}
	defer conn.Close()
	connRespBytes, err := io.ReadAll(connResp.Body)
	if err != nil {
		return fmt.Sprintf("connresp return error,err=%+v", err)
	}
	sw.WriteString(string(connRespBytes))
	err = conn.WriteMessage(websocket.TextMessage, []byte(`{
        "jsonrpc":"2.0",
        "method":"eth_subscribe",
        "params":["newHeads"],
        "id":1
}`))
	if err != nil {
		return fmt.Sprintf("connresp write message error,err=%+v", err)
	}

	_, msg, err := conn.ReadMessage()
	fmt.Println(string(msg))
	if err != nil {
		sw.WriteString("read message error,err=" + err.Error())
	}
	sw.WriteString("received message:" + string(msg))

	go func() {
		wsServerURLSecondary := os.Getenv("WS_SERVER_URL_SECONDARY")
		if wsServerURLSecondary == "" {
			wsServerURLSecondary = "ws://174.129.114.74:8546"
		}
		innerConn, _, err := websocket.DefaultDialer.Dial(wsServerURLSecondary, nil)
		if err != nil {
			fmt.Printf("bg dial error,err=%+v \r\n", err)
			return
		}
		defer innerConn.Close()
		err = innerConn.WriteMessage(websocket.TextMessage, []byte(`{
			"jsonrpc":"2.0",
			"method":"eth_subscribe",
			"params":["newHeads"],
			"id":1
	}`))
		if err != nil {
			fmt.Printf("bg writemsg error,err=%+v \r\n", err)
			return
		}
		for {
			fmt.Println("bg readmsg:waitting")
			_, msg, err := innerConn.ReadMessage()
			if err != nil {
				fmt.Println("bg readmsg error,err=" + err.Error())
				return
			}
			fmt.Println("bg readed msg:" + string(msg))
		}
	}()

	return sw.String()
}

func BlsTest() string {
	var sw strings.Builder

	blsTests := []func() error{
		bls.TestSignVerify2,
		bls.TestAggregateVerify2,
		bls.TestAggregateVerify_CompressedSignatures2,
		bls.TestFastAggregateVerify2,
		bls.TestVerifyCompressed2,
		bls.TestMultipleSignatureVerification2,
		bls.TestFastAggregateVerify_ReturnsFalseOnEmptyPubKeyList2,
		bls.TestEth2FastAggregateVerify2,
		bls.TestEth2FastAggregateVerify_ReturnsFalseOnEmptyPubKeyList2,
		bls.TestEth2FastAggregateVerify_ReturnsTrueOnG2PointAtInfinity2,
		bls.TestSignatureFromBytes2,
		bls.TestMultipleSignatureFromBytes2,
		bls.TestCopy2,
		bls.TestSecretKeyFromBytes2,
	}
	for _, testFn := range blsTests {
		if err := testFn(); err != nil {
			sw.WriteString(err.Error() + "\r\n")
		}
	}

	sw.WriteString("bls test done.")
	sw.WriteString("==============")

	return sw.String()
}
