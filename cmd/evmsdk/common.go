// Copyright 2021-2026 The N42 Authors
// This file is part of the N42 library.

package evmsdk

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/log"
)

const (
	VERSION            = ""
	EngineStateRunning = "running"
	EngineStateStopped = "stopped"

	RequestPath = ""
	ConfigPath  = "evm"
	LogFile     = "vertification_debug_log.txt"
)

var isDebug = true

var EE *EvmEngine = &EvmEngine{}

// Emit is the main entry point for the mobile SDK. It dispatches JSON requests
// to the appropriate engine methods and returns JSON responses.
func Emit(jsonText string) string {
	defer func() {
		if err := recover(); err != nil {
			simpleLog("engine panic,err=%+v", err)
			if err := EE.Stop(); err != nil {
				simpleLog("engine stop error,err=%+v", err)
			}
		}
	}()

	er := new(EmitResponse)
	eq := new(EmitRequest)
	if err := json.Unmarshal([]byte(jsonText), eq); err != nil {
		er.Code = 1
		er.Message = err.Error()
		return emitJSON(er)
	}

	if eq.Val != nil {
		simpleLog("emit:", eq.Typ, eq.Val)
	} else {
		simpleLog("emit:", eq.Typ)
	}

	switch eq.Typ {
	case "start":
		if err := EE.Start(); err != nil {
			er.Code, er.Message = 1, err.Error()
			if innerErr, ok := err.(*InnerError); ok {
				er.Code = innerErr.Code
				er.Message = innerErr.Msg
			}
			return emitJSON(er)
		}
	case "stop":
		if err := EE.Stop(); err != nil {
			er.Code, er.Message = 1, err.Error()
			return emitJSON(er)
		}
	case "setting":
		if err := EE.Setting(eq); err != nil {
			er.Code, er.Message = 1, err.Error()
			return emitJSON(er)
		}
	case "list":
		data, err := EE.List()
		if err != nil {
			er.Code, er.Message = 1, err.Error()
			return emitJSON(er)
		}
		er.Data = data
	case "blssign":
		data, err := EE.BlsSign(eq.Val)
		if err != nil {
			er.Code, er.Message = 1, err.Error()
			return emitJSON(er)
		}
		er.Data = data
	case "blspubk":
		data, err := EE.BlsPublicKey(eq.Val)
		if err != nil {
			er.Code, er.Message = 1, err.Error()
			return emitJSON(er)
		}
		er.Data = data
	case "state":
		er.Data = EE.State()
	case "decrypt":
		data, err := EE.Decrypt(eq)
		if err != nil {
			er.Code, er.Message = 1, err.Error()
			return emitJSON(er)
		}
		er.Data = data
	case "encrypt":
		data, err := EE.Encrypt(eq)
		if err != nil {
			er.Code, er.Message = 1, err.Error()
			return emitJSON(er)
		}
		er.Data = data
	case "test":
		er.Message = Test()
	}

	return emitJSON(er)
}

func emitJSON(j interface{}) string {
	a, _ := json.Marshal(j)
	return string(a)
}

func (e *EvmEngine) initLogger(lvl string) {
	isDebug = lvl == "debug"
	if len(lvl) != 0 {
		ensureLogFileExisted()
		log.InitMobileLogger(filepath.Join(EE.AppBasePath, LogFile), isDebug)
	}
}

func ensureLogFileExisted() {
	logFilePath := filepath.Join(EE.AppBasePath, LogFile)
	f, err := os.OpenFile(logFilePath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		fmt.Printf("create log file error,err=%+v\n", err)
		return
	}
	if err = f.Close(); err != nil {
		fmt.Printf("log file close error,err=%+v\n", err)
	}
}

func simpleLog(txt string, params ...interface{}) {
	if isDebug {
		ifces := make([]interface{}, 0, len(params)+1)
		ifces = append(ifces, txt)
		ifces = append(ifces, params...)
		fmt.Println(ifces...)
		log.Debug(txt, params...)
	}
}

func simpleLogf(txt string, params ...interface{}) {
	if isDebug {
		fmt.Printf(txt+"\n", params...)
		log.Debugf(txt, params...)
	}
}

// --- Types ---

type Setting struct {
	Height      int
	AppBasePath string
	Account     string

	PrivKey string `json:"-"`
}

type EmitRequest struct {
	Typ string      `json:"type"`
	Val interface{} `json:"val"`
}

type EmitResponse struct {
	Code    int         `json:"code"`
	Message string      `json:"message"`
	Data    interface{} `json:"data"`
}

type EvmEngine struct {
	mu         sync.Mutex         `json:"-"`
	ctx        context.Context    `json:"-"`
	cancelFunc context.CancelFunc `json:"-"`
	errChan    chan []error       `json:"-"`

	Account     string      `json:"account"`
	AppBasePath string      `json:"app_base_path"`
	EngineState string      `json:"state"`
	BlockChan   chan string `json:"-"`
	PrivKey     string      `json:"priv_key"`
	ServerUri   string      `json:"server_uri"`
	LogLevel    string      `json:"log_level"`
}

type AccountReward struct {
	Account string
	Number  *uint256.Int
	Value   *uint256.Int
}

type AccountRewards []*AccountReward

func (r AccountRewards) Len() int           { return len(r) }
func (r AccountRewards) Less(i, j int) bool { return strings.Compare(r[i].Account, r[j].Account) > 0 }
func (r AccountRewards) Swap(i, j int)      { r[i], r[j] = r[j], r[i] }

type InnerError struct {
	Code int
	Msg  string
}

func (i InnerError) Error() string { return fmt.Sprintf("code:%+v,msg:%+v", i.Code, i.Msg) }

// --- Engine lifecycle ---

func (e *EvmEngine) Start() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.EngineState == EngineStateRunning {
		return fmt.Errorf("evme is running")
	}
	e.ctx, e.cancelFunc = context.WithCancel(context.Background())
	e.EngineState = EngineStateRunning

	if err := e.verificationTaskBg(); err != nil {
		e.EngineState = EngineStateStopped
		if e.cancelFunc != nil {
			e.cancelFunc()
		}
		e.ctx = nil
		e.cancelFunc = nil
		simpleLogf("launch verification task failed,err=%+v", err.Error())
		return err
	}
	return nil
}

func (e *EvmEngine) Stop() error {
	e.mu.Lock()
	e.EngineState = EngineStateStopped
	if e.cancelFunc != nil {
		e.cancelFunc()
	}
	e.mu.Unlock()
	return nil
}

func (e *EvmEngine) State() string {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.EngineState == EngineStateRunning {
		return "started"
	}
	return "stopped"
}

func (e *EvmEngine) Setting(req *EmitRequest) error {
	m, ok := req.Val.(map[string]interface{})
	if !ok {
		return fmt.Errorf("type assert error,any==>smap")
	}

	e.mu.Lock()
	if str, ok := getStringField(m, "app_base_path"); ok {
		e.AppBasePath = str
	}
	e.mu.Unlock()

	if str, ok := getStringField(m, "priv_key"); ok {
		e.PrivKey = str
	}
	if str, ok := getStringField(m, "server_uri"); ok {
		e.ServerUri = str
	}
	if str, ok := getStringField(m, "account"); ok {
		e.Account = str
	}
	if str, ok := getStringField(m, "log_level"); ok {
		e.initLogger(str)
	}

	return nil
}

// getStringField extracts a string value from a map[string]interface{}.
func getStringField(m map[string]interface{}, key string) (string, bool) {
	v, ok := m[key]
	if !ok {
		return "", false
	}
	s, ok := v.(string)
	return s, ok
}

func (e *EvmEngine) List() (interface{}, error) {
	type l struct {
		A string `json:"a"`
		B bool   `json:"b"`
		C int    `json:"c"`
	}

	ll := []*l{
		{A: "a", B: true, C: 123},
		{A: "aa", B: false, C: 456},
	}
	return ll, nil
}

func (e *EvmEngine) SaveFile() error   { return nil }
func (e *EvmEngine) ReloadFile() error { return nil }

func (e *EvmEngine) BlsSign(req interface{}) (interface{}, error) {
	blsSignMap, ok := req.(map[string]interface{})
	if !ok {
		return nil, fmt.Errorf("empty input value")
	}

	privKey := e.PrivKey
	if str, ok := getStringField(blsSignMap, "priv_key"); ok {
		privKey = str
	}

	msg, _ := getStringField(blsSignMap, "msg")

	return BlsSign(privKey, msg)
}

func (e *EvmEngine) BlsPublicKey(req interface{}) (interface{}, error) {
	blsPublicKeyMap, ok := req.(map[string]interface{})
	if !ok {
		return nil, fmt.Errorf("empty input value")
	}

	privKey := e.PrivKey
	if str, ok := getStringField(blsPublicKeyMap, "priv_key"); ok {
		privKey = str
	}

	return BlsPublicKey(privKey)
}
