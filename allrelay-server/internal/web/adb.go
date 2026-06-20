package web

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const (
	adbDashboardPort           = 5555
	adbDashboardTimeoutSeconds = 15 * 60
	phoneControlPort           = 5008
)

type ADBStatus struct {
	IP              string `json:"ip,omitempty"`
	Port            int    `json:"port,omitempty"`
	PhoneReachable  bool   `json:"phoneReachable"`
	PhoneEnabled    bool   `json:"phoneEnabled"`
	PhoneListening  bool   `json:"phoneListening"`
	HostConnected   bool   `json:"hostConnected"`
	HostState       string `json:"hostState,omitempty"`
	AutoDisableAtMs int64  `json:"autoDisableAtMs,omitempty"`
	Message         string `json:"message,omitempty"`
}

type phoneADBStatus struct {
	OK              bool   `json:"ok"`
	IP              string `json:"ip"`
	ControlPort     int    `json:"controlPort"`
	Enabled         bool   `json:"enabled"`
	Listening       bool   `json:"listening"`
	Port            int    `json:"port"`
	Message         string `json:"message"`
	AutoDisableAtMs int64  `json:"autoDisableAtMs"`
}

func (ws *WebServer) handleADBConnect(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	status, err := ws.connectADB()
	if err != nil {
		ws.writeADBResponse(w, status, err)
		return
	}
	ws.writeADBResponse(w, status, nil)
}

func (ws *WebServer) handleADBDisconnect(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	status, err := ws.disconnectADB()
	if err != nil {
		ws.writeADBResponse(w, status, err)
		return
	}
	ws.writeADBResponse(w, status, nil)
}

func (ws *WebServer) handleADBStatus(w http.ResponseWriter, r *http.Request) {
	status := ws.queryADBStatus()
	ws.writeADBResponse(w, status, nil)
}

func (ws *WebServer) connectADB() (ADBStatus, error) {
	phone := ws.currentPhone()
	if phone == nil || strings.TrimSpace(phone.IP) == "" {
		return ADBStatus{Message: "Connect to a phone first"}, fmt.Errorf("connect to a phone first")
	}
	if _, err := exec.LookPath("adb"); err != nil {
		return ADBStatus{IP: phone.IP, Port: adbDashboardPort, Message: "adb not found on Ubuntu host"}, fmt.Errorf("adb not found on Ubuntu host")
	}

	phoneStatus, err := ws.callPhoneADB(phone.IP, http.MethodPost, "/adb/enable", map[string]any{
		"port":           adbDashboardPort,
		"timeoutSeconds": adbDashboardTimeoutSeconds,
	})
	if err != nil {
		return ADBStatus{IP: phone.IP, Port: adbDashboardPort, Message: "Open the AllRelay app on the phone once so dashboard ADB control is available"}, err
	}
	if !phoneStatus.Listening {
		phoneStatus, err = ws.waitForPhoneADB(phone.IP, 8*time.Second)
		if err != nil {
			status := ws.buildADBStatus(phone.IP, phoneStatus)
			return status, err
		}
	}

	out, cmdErr := ws.runADBCommand("connect", fmt.Sprintf("%s:%d", phone.IP, adbDashboardPort))
	slog.Info("ADB connect attempted", "ip", phone.IP, "port", adbDashboardPort, "output", strings.TrimSpace(out))
	status := ws.buildADBStatus(phone.IP, phoneStatus)

	if cmdErr != nil {
		status.Message = strings.TrimSpace(out)
		if status.Message == "" {
			status.Message = cmdErr.Error()
		}
		return status, fmt.Errorf(status.Message)
	}

	status = ws.queryADBStatus()
	if status.HostState == "unauthorized" {
		authStatus, authErr := ws.authorizeHostKey(phone.IP)
		if authErr != nil {
			status.Message = authStatus.Message
			return status, authErr
		}
		_, _ = ws.runADBCommand("disconnect", fmt.Sprintf("%s:%d", phone.IP, adbDashboardPort))
		out2, _ := ws.runADBCommand("connect", fmt.Sprintf("%s:%d", phone.IP, adbDashboardPort))
		slog.Info("ADB connect retry after authorize", "ip", phone.IP, "port", adbDashboardPort, "output", strings.TrimSpace(out2))
		status = ws.queryADBStatus()
	}

	if status.HostState == "unauthorized" {
		status.Message = "ADB host key not yet authorized on the phone"
		return status, fmt.Errorf(status.Message)
	}
	if !status.HostConnected {
		if strings.TrimSpace(out) != "" {
			status.Message = strings.TrimSpace(out)
		} else if status.Message == "" {
			status.Message = "adb connect did not produce a connected device state"
		}
		return status, fmt.Errorf(status.Message)
	}

	status.Message = fmt.Sprintf("ADB connected to %s:%d", phone.IP, adbDashboardPort)
	return status, nil
}

func (ws *WebServer) authorizeHostKey(phoneIP string) (ADBStatus, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return ADBStatus{IP: phoneIP, Port: adbDashboardPort, Message: "Cannot find home directory"}, err
	}
	keyPath := filepath.Join(home, ".android", "adbkey.pub")
	keyBytes, err := os.ReadFile(keyPath)
	if err != nil {
		return ADBStatus{IP: phoneIP, Port: adbDashboardPort, Message: "Host adb key not found. Run 'adb keygen ~/.android/adbkey' first."}, err
	}
	key := strings.TrimSpace(string(keyBytes))
	_, phoneErr := ws.callPhoneADB(phoneIP, http.MethodPost, "/adb/authorize", map[string]any{"key": key})
	if phoneErr != nil {
		return ADBStatus{IP: phoneIP, Port: adbDashboardPort, Message: "Phone failed to authorize host key"}, phoneErr
	}
	return ADBStatus{IP: phoneIP, Port: adbDashboardPort, Message: "Host key authorized on phone"}, nil
}

func (ws *WebServer) disconnectADB() (ADBStatus, error) {
	phone := ws.currentPhone()
	if phone == nil || strings.TrimSpace(phone.IP) == "" {
		return ADBStatus{Message: "Connect to a phone first"}, fmt.Errorf("connect to a phone first")
	}
	if _, err := exec.LookPath("adb"); err != nil {
		return ADBStatus{IP: phone.IP, Port: adbDashboardPort, Message: "adb not found on Ubuntu host"}, fmt.Errorf("adb not found on Ubuntu host")
	}

	out, cmdErr := ws.runADBCommand("disconnect", fmt.Sprintf("%s:%d", phone.IP, adbDashboardPort))
	slog.Info("ADB disconnect attempted", "ip", phone.IP, "port", adbDashboardPort, "output", strings.TrimSpace(out))

	phoneStatus, phoneErr := ws.callPhoneADB(phone.IP, http.MethodPost, "/adb/disable", nil)
	status := ws.buildADBStatus(phone.IP, phoneStatus)
	if phoneErr != nil {
		status.Message = "Host disconnected, but failed to disable phone-side ADB TCP"
		return status, phoneErr
	}

	status = ws.queryADBStatus()
	status.Message = "ADB disconnected and phone-side ADB TCP disabled"
	if cmdErr != nil && !strings.Contains(strings.ToLower(out), "no such device") {
		return status, fmt.Errorf(strings.TrimSpace(out))
	}
	return status, nil
}

func (ws *WebServer) queryADBStatus() ADBStatus {
	phone := ws.currentPhone()
	if phone == nil || strings.TrimSpace(phone.IP) == "" {
		return ADBStatus{Message: "Connect to a phone first"}
	}

	phoneStatus, phoneErr := ws.callPhoneADB(phone.IP, http.MethodGet, "/adb/status", nil)
	status := ws.buildADBStatus(phone.IP, phoneStatus)
	if phoneErr != nil {
		status.Message = "Open the AllRelay app on the phone once so dashboard ADB control is available"
	}

	if _, err := exec.LookPath("adb"); err != nil {
		if status.Message == "" {
			status.Message = "adb not found on Ubuntu host"
		}
		return status
	}

	state, err := ws.lookupADBDeviceState(phone.IP, adbDashboardPort)
	if err == nil {
		status.HostState = state
		status.HostConnected = state == "device"
	}

	status.Message = defaultADBMessage(status)
	return status
}

func (ws *WebServer) buildADBStatus(phoneIP string, phoneStatus phoneADBStatus) ADBStatus {
	status := ADBStatus{
		IP:              phoneIP,
		Port:            adbDashboardPort,
		PhoneReachable:  phoneStatus.OK,
		PhoneEnabled:    phoneStatus.Enabled,
		PhoneListening:  phoneStatus.Listening,
		AutoDisableAtMs: phoneStatus.AutoDisableAtMs,
		Message:         phoneStatus.Message,
	}
	if phoneStatus.Port > 0 {
		status.Port = phoneStatus.Port
	}
	if status.Message == "" {
		status.Message = defaultADBMessage(status)
	}
	return status
}

func defaultADBMessage(status ADBStatus) string {
	switch {
	case status.HostConnected:
		return fmt.Sprintf("ADB connected to %s:%d", status.IP, status.Port)
	case status.HostState == "unauthorized":
		return "ADB host key not yet authorized on the phone"
	case status.PhoneEnabled && status.PhoneListening:
		return fmt.Sprintf("Phone-side ADB TCP is enabled on %s:%d", status.IP, status.Port)
	case status.PhoneEnabled:
		return "Phone-side ADB TCP is enabling"
	default:
		return "ADB disabled"
	}
}

func (ws *WebServer) writeADBResponse(w http.ResponseWriter, status ADBStatus, err error) {
	code := http.StatusOK
	if err != nil {
		code = http.StatusBadGateway
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(status)
}

func (ws *WebServer) currentPhone() *PhoneDevice {
	ws.mu.RLock()
	defer ws.mu.RUnlock()
	if ws.currentConn == nil || ws.currentConn.Phone == nil {
		return nil
	}
	phone := *ws.currentConn.Phone
	return &phone
}

func (ws *WebServer) waitForPhoneADB(phoneIP string, timeout time.Duration) (phoneADBStatus, error) {
	deadline := time.Now().Add(timeout)
	var last phoneADBStatus
	for time.Now().Before(deadline) {
		status, err := ws.callPhoneADB(phoneIP, http.MethodGet, "/adb/status", nil)
		if err == nil {
			last = status
			if status.Listening {
				return status, nil
			}
		}
		time.Sleep(500 * time.Millisecond)
	}
	if !last.OK {
		last.Message = "Phone-side ADB TCP did not become reachable in time"
	}
	return last, fmt.Errorf("phone-side ADB TCP did not become reachable in time")
}

func (ws *WebServer) callPhoneADB(phoneIP, method, path string, payload map[string]any) (phoneADBStatus, error) {
	var reqBody io.Reader
	if payload != nil {
		buf := &bytes.Buffer{}
		if err := json.NewEncoder(buf).Encode(payload); err != nil {
			return phoneADBStatus{}, err
		}
		reqBody = buf
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	url := fmt.Sprintf("http://%s:%d%s", phoneIP, phoneControlPort, path)
	req, err := http.NewRequestWithContext(ctx, method, url, reqBody)
	if err != nil {
		return phoneADBStatus{}, err
	}
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		return phoneADBStatus{}, err
	}
	defer resp.Body.Close()

	var status phoneADBStatus
	if err := json.NewDecoder(resp.Body).Decode(&status); err != nil {
		return phoneADBStatus{}, err
	}
	if resp.StatusCode >= 400 || !status.OK {
		if strings.TrimSpace(status.Message) == "" {
			status.Message = fmt.Sprintf("phone control API returned HTTP %d", resp.StatusCode)
		}
		return status, fmt.Errorf(status.Message)
	}
	return status, nil
}

func (ws *WebServer) lookupADBDeviceState(ip string, port int) (string, error) {
	out, err := ws.runADBCommand("devices")
	if err != nil {
		return "", err
	}
	serial := fmt.Sprintf("%s:%d", ip, port)
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "List of devices attached") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) >= 2 && fields[0] == serial {
			return fields[1], nil
		}
	}
	return "disconnected", nil
}

func (ws *WebServer) runADBCommand(args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "adb", args...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}
