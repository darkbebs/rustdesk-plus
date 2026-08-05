package main

// Instalador RustDesk Plus — janela Win32 nativa profissional.
//
// Build (no diretório b:\Newrust):
//   go build -C installer -H windowsgui ^
//     -ldflags "-X main.serverIP=IP -X main.serverKey=KEY -X main.apiURL=URL -X main.tenantID=UUID -X main.unattendedPassword=PASS" ^
//     -o ..\rustdesk-installer.exe .

import (
	"context"
	_ "embed"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// ── Config injetado em build ──────────────────────────────────────────────────
var (
	serverIP           = ""
	serverKey          = ""
	apiURL             = ""
	unattendedPassword = ""
	tenantID           = ""
	installCode        = ""
	agentEnabled       = "false" // injetado no build; "true" instala o agente de gerenciamento
	buildID            = ""      // INSTALLER_BUILD; aparece no log para identificar o binario
)

const rustdeskDownload = "https://github.com/rustdesk/rustdesk/releases/download/1.3.9/rustdesk-1.3.9-x86_64.exe"
var rustdeskExe = `C:\Program Files\RustDesk\rustdesk.exe`
const agentDir = `C:\Program Files\RustDesk Plus`
const agentExe = agentDir + `\rustdesk-agent.exe`
const agentTask = "RustDeskPlusAgent"

//go:embed rustdesk-agent.exe
var embeddedAgent []byte

// ── Win32 DLLs + Procs ───────────────────────────────────────────────────────
var (
	user32   = windows.NewLazySystemDLL("user32.dll")
	kernel32 = windows.NewLazySystemDLL("kernel32.dll")
	gdi32    = windows.NewLazySystemDLL("gdi32.dll")
	comctl32 = windows.NewLazySystemDLL("comctl32.dll")
	shell32  = windows.NewLazySystemDLL("shell32.dll")

	_RegisterClassExW     = user32.NewProc("RegisterClassExW")
	_CreateWindowExW      = user32.NewProc("CreateWindowExW")
	_ShowWindow           = user32.NewProc("ShowWindow")
	_UpdateWindow         = user32.NewProc("UpdateWindow")
	_GetMessageW          = user32.NewProc("GetMessageW")
	_TranslateMessage     = user32.NewProc("TranslateMessage")
	_DispatchMessageW     = user32.NewProc("DispatchMessageW")
	_DefWindowProcW       = user32.NewProc("DefWindowProcW")
	_PostQuitMessage      = user32.NewProc("PostQuitMessage")
	_PostMessageW         = user32.NewProc("PostMessageW")
	_SendMessageW         = user32.NewProc("SendMessageW")
	_SetWindowTextW       = user32.NewProc("SetWindowTextW")
	_EnableWindow         = user32.NewProc("EnableWindow")
	_MessageBoxW          = user32.NewProc("MessageBoxW")
	_LoadCursorW          = user32.NewProc("LoadCursorW")
	_GetModuleHandleW     = kernel32.NewProc("GetModuleHandleW")
	_CreateSolidBrush     = gdi32.NewProc("CreateSolidBrush")
	_DeleteObject         = gdi32.NewProc("DeleteObject")
	_FillRect             = user32.NewProc("FillRect")
	_BeginPaint           = user32.NewProc("BeginPaint")
	_EndPaint             = user32.NewProc("EndPaint")
	_SetTextColor         = gdi32.NewProc("SetTextColor")
	_SetBkColor           = gdi32.NewProc("SetBkColor")
	_SetBkMode            = gdi32.NewProc("SetBkMode")
	_CreateFontW          = gdi32.NewProc("CreateFontW")
	_SelectObject         = gdi32.NewProc("SelectObject")
	_TextOutW             = gdi32.NewProc("TextOutW")
	_GetClientRect        = user32.NewProc("GetClientRect")
	_GetSystemMetrics     = user32.NewProc("GetSystemMetrics")
	_InitCommonControlsEx = comctl32.NewProc("InitCommonControlsEx")
	_ShellExecuteW        = shell32.NewProc("ShellExecuteW")
	_InvalidateRect       = user32.NewProc("InvalidateRect")
	_Ellipse              = gdi32.NewProc("Ellipse")
	_CreatePen            = gdi32.NewProc("CreatePen")
	_GetStockObject       = gdi32.NewProc("GetStockObject")
	_Rectangle            = gdi32.NewProc("Rectangle")
	_SetPixel             = gdi32.NewProc("SetPixel")
	_MoveToEx             = gdi32.NewProc("MoveToEx")
	_LineTo               = gdi32.NewProc("LineTo")
	_CreateCompatibleDC   = gdi32.NewProc("CreateCompatibleDC")
	_CreateCompatibleBmp  = gdi32.NewProc("CreateCompatibleBitmap")
	_BitBlt               = gdi32.NewProc("BitBlt")
	_DeleteDC             = gdi32.NewProc("DeleteDC")
)

// ── Constantes Win32 ─────────────────────────────────────────────────────────
const (
	WS_OVERLAPPED       = 0x00000000
	WS_CAPTION          = 0x00C00000
	WS_SYSMENU          = 0x00080000
	WS_MINIMIZEBOX      = 0x00020000
	WS_VISIBLE          = 0x10000000
	WS_CHILD            = 0x40000000
	WS_OVERLAPPEDWINDOW = WS_OVERLAPPED | WS_CAPTION | WS_SYSMENU | WS_MINIMIZEBOX

	BS_DEFPUSHBUTTON = 0x00000001

	WM_CREATE         = 0x0001
	WM_DESTROY        = 0x0002
	WM_PAINT          = 0x000F
	WM_COMMAND        = 0x0111
	WM_CLOSE          = 0x0010
	WM_CTLCOLORSTATIC = 0x0138
	WM_ERASEBKGND     = 0x0014
	SRCCOPY           = 0x00CC0020
	WM_USER           = 0x0400
	WM_APP_STATUS     = WM_USER + 1
	WM_APP_PROGRESS   = WM_USER + 2
	WM_APP_DONE       = WM_USER + 3
	WM_APP_ERROR      = WM_USER + 4
	WM_APP_STEP       = WM_USER + 5

	IDC_ARROW         = 32512
	ICC_PROGRESS_CLASS = 0x20
	TRANSPARENT        = 1
	OPAQUE             = 2
	SW_SHOW            = 5
	NULL_BRUSH         = 5
	PS_SOLID           = 0

	ID_BTN  = 101
	ID_STAT = 103

	MB_OK        = 0x00000000
	MB_ICONERROR = 0x00000010
	MB_ICONINFO  = 0x00000040
)

// ── Cores (Win32 BGR) ─────────────────────────────────────────────────────────
const (
	clrHeaderBg  = 0x002A170F // #0F172A slate-900
	clrHeaderBdr = 0x00332620 // #202633 slightly lighter
	clrBodyBg    = 0x00FCFAF8 // #F8FAFC slate-50
	clrCardBg    = 0x00FFFFFF // white
	clrAccent    = 0x00EB6325 // #2563EB blue-600
	clrAccentDk  = 0x00B85115 // #1551B8 blue-700
	clrSuccess   = 0x0081B910 // #10B981 emerald-500
	clrPending   = 0x00D4C4B8 // #B8C4D4 slate-300
	clrText      = 0x00332920 // #202933 slate-800
	clrSubtext   = 0x00B8A394 // #94A3B8 slate-400
	clrMuted     = 0x008B7464 // #64748B slate-500
	clrBorder    = 0x00E8E0DC // #DCE0E8 slate-200
	clrWhite     = 0x00FFFFFF
	clrProgBg    = 0x00E8E0DC // progress track
	clrError     = 0x006060DC // #DC6060
)

// ── Geometria ─────────────────────────────────────────────────────────────────
const (
	winW   = 560
	winH   = 430
	hdrH   = 96
	padX   = 44
	stepY0 = 118
	stepDY = 44
)

var stepLabels = [4]string{
	"Parar processos existentes",
	"Baixar e instalar RustDesk",
	"Configurar servidor e senha",
	"Ativar serviço de inicialização",
}

// ── Structs Win32 ─────────────────────────────────────────────────────────────
type WNDCLASSEX struct {
	CbSize        uint32
	Style         uint32
	LpfnWndProc   uintptr
	CbClsExtra    int32
	CbWndExtra    int32
	HInstance     uintptr
	HIcon         uintptr
	HCursor       uintptr
	HbrBackground uintptr
	LpszMenuName  *uint16
	LpszClassName *uint16
	HIconSm       uintptr
}

type MSG struct {
	Hwnd    uintptr
	Message uint32
	WParam  uintptr
	LParam  uintptr
	Time    uint32
	Pt      [2]int32
}

type RECT struct{ Left, Top, Right, Bottom int32 }

type PAINTSTRUCT struct {
	Hdc         uintptr
	FErase      int32
	RcPaint     RECT
	FRestore    int32
	FIncUpdate  int32
	RgbReserved [32]byte
}

type INITCOMMONCONTROLSEX struct {
	DwSize uint32
	DwICC  uint32
}

// ── Globals ───────────────────────────────────────────────────────────────────
var (
	hwndMain  uintptr
	hwndBtn   uintptr
	hwndStat  uintptr
	hInst     uintptr
	hBrushBg  uintptr // body bg
	hBrushHdr uintptr // header bg
	installing bool
	currentStep int // 0=idle 1-4=active step 5=done 6=error
	progressPct int // 0-100

	// Texto trocado entre a goroutine de instalacao e a UI. Nao da pra mandar
	// ponteiro Go via PostMessageW: como uintptr, o GC nao ve referencia e pode
	// liberar o buffer antes da thread da UI ler.
	msgMu     sync.Mutex
	statusMsg string
	errorMsg  string
)

func setSharedMsg(dst *string, s string) {
	msgMu.Lock()
	*dst = s
	msgMu.Unlock()
}

func getSharedMsg(src *string) string {
	msgMu.Lock()
	defer msgMu.Unlock()
	return *src
}

// Uma janela Win32 pertence a thread que a criou, e so essa thread pode bombear
// a fila de mensagens dela. O scheduler do Go migra goroutines entre threads do
// SO livremente — sem travar a goroutine principal, o GetMessageW do loop pode
// acordar em outra thread, passar a ler uma fila vazia e deixar a fila real da
// janela sem ser drenada: a janela congela ("Nao esta respondendo").
//
// Precisa ser em init(), que roda na goroutine principal antes do main().
func init() {
	runtime.LockOSThread()
}

// ── Entry Point ───────────────────────────────────────────────────────────────
func main() {
	if serverIP == "" || tenantID == "" {
		msgBox(0, "Este instalador não foi configurado corretamente.\n\nBaixe o instalador pelo painel de gerenciamento.", "Erro de Configuração", MB_ICONERROR|MB_OK)
		return
	}

	if !windows.GetCurrentProcessToken().IsElevated() {
		relaunchAsAdmin()
		return
	}

	logf("=== instalador iniciado (build %q, tenant %s) ===", buildID, tenantID)

	icc := INITCOMMONCONTROLSEX{DwSize: 8, DwICC: ICC_PROGRESS_CLASS}
	_InitCommonControlsEx.Call(uintptr(unsafe.Pointer(&icc)))

	hInst, _, _ = _GetModuleHandleW.Call(0)
	hBrushBg, _, _ = _CreateSolidBrush.Call(clrBodyBg)
	hBrushHdr, _, _ = _CreateSolidBrush.Call(clrHeaderBg)

	cursor, _, _ := _LoadCursorW.Call(0, IDC_ARROW)
	className, _ := windows.UTF16PtrFromString("RDPlusInstaller")
	wc := WNDCLASSEX{
		CbSize:        uint32(unsafe.Sizeof(WNDCLASSEX{})),
		LpfnWndProc:   syscall.NewCallback(wndProc),
		HInstance:     hInst,
		HCursor:       cursor,
		HbrBackground: hBrushBg,
		LpszClassName: className,
	}
	_RegisterClassExW.Call(uintptr(unsafe.Pointer(&wc)))

	sm_cx, _, _ := _GetSystemMetrics.Call(0)
	sm_cy, _, _ := _GetSystemMetrics.Call(1)
	x := (int(sm_cx) - winW) / 2
	y := (int(sm_cy) - winH) / 2

	title, _ := windows.UTF16PtrFromString("RustDesk Plus — Instalação")
	hwndMain, _, _ = _CreateWindowExW.Call(
		0,
		uintptr(unsafe.Pointer(className)),
		uintptr(unsafe.Pointer(title)),
		WS_OVERLAPPEDWINDOW&^0x00040000,
		uintptr(x), uintptr(y),
		winW, winH,
		0, 0, hInst, 0,
	)

	_ShowWindow.Call(hwndMain, SW_SHOW)
	_UpdateWindow.Call(hwndMain)

	var msg MSG
	for {
		r, _, _ := _GetMessageW.Call(uintptr(unsafe.Pointer(&msg)), 0, 0, 0)
		if r == 0 {
			break
		}
		_TranslateMessage.Call(uintptr(unsafe.Pointer(&msg)))
		_DispatchMessageW.Call(uintptr(unsafe.Pointer(&msg)))
	}
}

// ── Window Procedure ──────────────────────────────────────────────────────────
func wndProc(hwnd, msg, wParam, lParam uintptr) uintptr {
	switch msg {
	case WM_CREATE:
		createControls(hwnd)

	case WM_ERASEBKGND:
		// paintAll pinta a janela inteira; apagar o fundo antes so causa flicker.
		return 1

	case WM_PAINT:
		var ps PAINTSTRUCT
		hdc, _, _ := _BeginPaint.Call(hwnd, uintptr(unsafe.Pointer(&ps)))
		// Double-buffer: pinta num bitmap na memoria e copia de uma vez.
		memDC, _, _ := _CreateCompatibleDC.Call(hdc)
		if memDC != 0 {
			bmp, _, _ := _CreateCompatibleBmp.Call(hdc, winW, winH)
			if bmp != 0 {
				oldBmp, _, _ := _SelectObject.Call(memDC, bmp)
				paintAll(memDC)
				_BitBlt.Call(hdc, 0, 0, winW, winH, memDC, 0, 0, SRCCOPY)
				_SelectObject.Call(memDC, oldBmp)
				_DeleteObject.Call(bmp)
			} else {
				paintAll(hdc)
			}
			_DeleteDC.Call(memDC)
		} else {
			paintAll(hdc)
		}
		_EndPaint.Call(hwnd, uintptr(unsafe.Pointer(&ps)))
		return 0

	case WM_CTLCOLORSTATIC:
		_SetBkMode.Call(wParam, TRANSPARENT)
		_SetTextColor.Call(wParam, clrMuted)
		_SetBkColor.Call(wParam, clrBodyBg)
		return hBrushBg

	case WM_COMMAND:
		id := wParam & 0xFFFF
		if id == ID_BTN && !installing {
			installing = true
			_EnableWindow.Call(hwndBtn, 0)
			setWinText(hwndBtn, "Instalando...")
			go runInstall(hwnd)
		}

	case WM_APP_STATUS:
		// Logado tambem aqui, na thread da UI: se as linhas "worker" continuarem
		// e as "ui" pararem, o travamento e do message loop, nao do download.
		s := getSharedMsg(&statusMsg)
		logf("ui      %s", s)
		setWinText(hwndStat, s)

	case WM_APP_PROGRESS:
		progressPct = int(wParam)
		_InvalidateRect.Call(hwnd, 0, 0)

	case WM_APP_STEP:
		currentStep = int(wParam)
		_InvalidateRect.Call(hwnd, 0, 0)

	case WM_APP_DONE:
		currentStep = 5
		progressPct = 100
		_InvalidateRect.Call(hwnd, 0, 0)
		setWinText(hwndBtn, "Concluído  ✓")
		_EnableWindow.Call(hwndBtn, 1)
		msgBox(hwnd,
			"Instalação concluída com sucesso!\n\nEste PC aparecerá no painel de gerenciamento em instantes.\n\nO RustDesk está ativo como serviço do Windows e iniciará automaticamente.",
			"Instalação Concluída", MB_ICONINFO|MB_OK)
		_PostQuitMessage.Call(0)

	case WM_APP_ERROR:
		text := getSharedMsg(&errorMsg)
		currentStep = 6
		progressPct = 0
		installing = false
		_InvalidateRect.Call(hwnd, 0, 0)
		_EnableWindow.Call(hwndBtn, 1)
		setWinText(hwndBtn, "Tentar novamente")
		msgBox(hwnd, "Falha na instalação:\n\n"+text, "Erro", MB_ICONERROR|MB_OK)

	case WM_CLOSE:
		_PostQuitMessage.Call(0)
	}
	r, _, _ := _DefWindowProcW.Call(hwnd, msg, wParam, lParam)
	return r
}

// ── Criar controles ───────────────────────────────────────────────────────────
func createControls(hwnd uintptr) {
	staticClass, _ := windows.UTF16PtrFromString("STATIC")
	btnClass, _ := windows.UTF16PtrFromString("BUTTON")

	// Status label (abaixo das etapas)
	statText, _ := windows.UTF16PtrFromString("Clique em Instalar para começar.")
	hwndStat, _, _ = _CreateWindowExW.Call(
		0, uintptr(unsafe.Pointer(staticClass)),
		uintptr(unsafe.Pointer(statText)),
		WS_CHILD|WS_VISIBLE,
		padX, stepY0+stepDY*4+8,
		uintptr(winW-padX*2), 22,
		hwnd, ID_STAT, hInst, 0,
	)
	hFont := createFont(12, false)
	_SendMessageW.Call(hwndStat, 0x0030, hFont, 1)

	// Botão Instalar — centralizado
	btnText, _ := windows.UTF16PtrFromString("    Instalar    ")
	btnW := 200
	btnH := 42
	btnX := (winW - btnW) / 2
	btnY := stepY0 + stepDY*4 + 50
	hwndBtn, _, _ = _CreateWindowExW.Call(
		0, uintptr(unsafe.Pointer(btnClass)),
		uintptr(unsafe.Pointer(btnText)),
		WS_CHILD|WS_VISIBLE|BS_DEFPUSHBUTTON,
		uintptr(btnX), uintptr(btnY),
		uintptr(btnW), uintptr(btnH),
		hwnd, ID_BTN, hInst, 0,
	)
	hFontBtn := createFont(13, true)
	_SendMessageW.Call(hwndBtn, 0x0030, hFontBtn, 1)
}

// ── Pintura principal ─────────────────────────────────────────────────────────
func paintAll(hdc uintptr) {
	// ── Header ────────────────────────────────────────────────────────────────
	hdrRect := RECT{0, 0, winW, int32(hdrH)}
	_FillRect.Call(hdc, uintptr(unsafe.Pointer(&hdrRect)), hBrushHdr)

	// Linha divisória sutil
	pen1, _, _ := _CreatePen.Call(PS_SOLID, 1, clrHeaderBdr)
	oldPen, _, _ := _SelectObject.Call(hdc, pen1)
	_MoveToEx.Call(hdc, 0, uintptr(hdrH-1), 0)
	_LineTo.Call(hdc, winW, uintptr(hdrH-1))
	_SelectObject.Call(hdc, oldPen)
	_DeleteObject.Call(pen1)

	// Dot decorativo azul
	dotBrush, _, _ := _CreateSolidBrush.Call(clrAccent)
	dotPen, _, _ := _CreatePen.Call(PS_SOLID, 0, clrAccent)
	oldDotBr, _, _ := _SelectObject.Call(hdc, dotBrush)
	oldDotPn, _, _ := _SelectObject.Call(hdc, dotPen)
	_Ellipse.Call(hdc, padX, 28, padX+12, 40)
	_SelectObject.Call(hdc, oldDotBr)
	_SelectObject.Call(hdc, oldDotPn)
	_DeleteObject.Call(dotBrush)
	_DeleteObject.Call(dotPen)

	// Título principal
	_SetBkMode.Call(hdc, TRANSPARENT)
	_SetTextColor.Call(hdc, clrWhite)
	hf1 := createFont(20, true)
	old1, _, _ := _SelectObject.Call(hdc, hf1)
	title, _ := windows.UTF16PtrFromString("RustDesk Plus")
	_TextOutW.Call(hdc, padX+20, 22, uintptr(unsafe.Pointer(title)), uintptr(len("RustDesk Plus")))
	_SelectObject.Call(hdc, old1)
	_DeleteObject.Call(hf1)

	// Subtítulo
	_SetTextColor.Call(hdc, clrSubtext)
	hf2 := createFont(12, false)
	old2, _, _ := _SelectObject.Call(hdc, hf2)
	sub, _ := windows.UTF16PtrFromString("Instalação de Acesso Remoto Gerenciado")
	_TextOutW.Call(hdc, padX+20, 50, uintptr(unsafe.Pointer(sub)), uintptr(len("Instalação de Acesso Remoto Gerenciado")))

	// Servidor
	_SetTextColor.Call(hdc, clrMuted)
	srv := "○  " + serverIP
	srvW, _ := windows.UTF16PtrFromString(srv)
	_TextOutW.Call(hdc, padX+20, 70, uintptr(unsafe.Pointer(srvW)), uintptr(len(srv)))
	_SelectObject.Call(hdc, old2)
	_DeleteObject.Call(hf2)

	// ── Corpo ─────────────────────────────────────────────────────────────────
	bodyRect := RECT{0, int32(hdrH), winW, winH}
	_FillRect.Call(hdc, uintptr(unsafe.Pointer(&bodyRect)), hBrushBg)

	// ── Etapas ────────────────────────────────────────────────────────────────
	for i, label := range stepLabels {
		paintStep(hdc, i, label)
	}

	// ── Barra de progresso ────────────────────────────────────────────────────
	if installing || currentStep == 5 {
		progY := stepY0 + stepDY*4 + 32
		progH := 6
		progW := winW - padX*2

		// Track
		trackBrush, _, _ := _CreateSolidBrush.Call(clrProgBg)
		trackR := RECT{int32(padX), int32(progY), int32(padX + progW), int32(progY + progH)}
		_FillRect.Call(hdc, uintptr(unsafe.Pointer(&trackR)), trackBrush)
		_DeleteObject.Call(trackBrush)

		// Fill
		fillW := progressPct * progW / 100
		if fillW > 0 {
			fillColor := clrAccent
			if currentStep == 5 {
				fillColor = clrSuccess
			}
			fillBrush, _, _ := _CreateSolidBrush.Call(uintptr(fillColor))
			fillR := RECT{int32(padX), int32(progY), int32(padX + fillW), int32(progY + progH)}
			_FillRect.Call(hdc, uintptr(unsafe.Pointer(&fillR)), fillBrush)
			_DeleteObject.Call(fillBrush)
		}
	}
}

func paintStep(hdc uintptr, idx int, label string) {
	cx := padX + 12
	cy := stepY0 + idx*stepDY + 12
	r := 12

	// Determina estado
	done := idx < currentStep && currentStep > 0 && currentStep != 6
	active := idx == currentStep-1 && currentStep > 0 && currentStep <= 4
	err := currentStep == 6 && idx == currentStep-1

	// Círculo
	var fillColor, penColor uintptr
	switch {
	case err:
		fillColor, penColor = clrError, clrError
	case done:
		fillColor, penColor = clrSuccess, clrSuccess
	case active:
		fillColor, penColor = clrAccent, clrAccent
	default:
		fillColor, penColor = clrBodyBg, clrPending
	}

	br, _, _ := _CreateSolidBrush.Call(fillColor)
	pn, _, _ := _CreatePen.Call(PS_SOLID, 2, penColor)
	// Restaurar os objetos antigos antes de deletar: DeleteObject falha em
	// objeto ainda selecionado no DC, e o handle vaza (limite de 10k por processo).
	oldBr, _, _ := _SelectObject.Call(hdc, br)
	oldPn, _, _ := _SelectObject.Call(hdc, pn)
	_Ellipse.Call(hdc, uintptr(cx-r), uintptr(cy-r), uintptr(cx+r), uintptr(cy+r))
	_SelectObject.Call(hdc, oldBr)
	_SelectObject.Call(hdc, oldPn)
	_DeleteObject.Call(br)
	_DeleteObject.Call(pn)

	// Número ou checkmark no círculo
	_SetBkMode.Call(hdc, TRANSPARENT)
	var sym string
	switch {
	case done:
		sym = "✓"
		_SetTextColor.Call(hdc, clrWhite)
	case active:
		sym = fmt.Sprintf("%d", idx+1)
		_SetTextColor.Call(hdc, clrWhite)
	default:
		sym = fmt.Sprintf("%d", idx+1)
		_SetTextColor.Call(hdc, clrPending)
	}
	hfsym := createFont(10, true)
	oldsym, _, _ := _SelectObject.Call(hdc, hfsym)
	symW, _ := windows.UTF16PtrFromString(sym)
	_TextOutW.Call(hdc, uintptr(cx-6), uintptr(cy-7), uintptr(unsafe.Pointer(symW)), uintptr(len(sym)))
	_SelectObject.Call(hdc, oldsym)
	_DeleteObject.Call(hfsym)

	// Texto da etapa
	var textColor uintptr
	var bold bool
	switch {
	case done:
		textColor, bold = clrMuted, false
	case active:
		textColor, bold = clrText, true
	default:
		textColor, bold = clrSubtext, false
	}
	_SetTextColor.Call(hdc, textColor)
	hftxt := createFont(13, bold)
	oldtxt, _, _ := _SelectObject.Call(hdc, hftxt)
	lw, _ := windows.UTF16PtrFromString(label)
	_TextOutW.Call(hdc, uintptr(padX+30), uintptr(cy-9), uintptr(unsafe.Pointer(lw)), uintptr(len(label)))
	_SelectObject.Call(hdc, oldtxt)
	_DeleteObject.Call(hftxt)

	// Linha conectora entre etapas
	if idx < len(stepLabels)-1 {
		lnBrush, _, _ := _CreateSolidBrush.Call(clrBorder)
		lnR := RECT{int32(cx - 1), int32(cy + r), int32(cx + 1), int32(cy + r + stepDY - r*2)}
		_FillRect.Call(hdc, uintptr(unsafe.Pointer(&lnR)), lnBrush)
		_DeleteObject.Call(lnBrush)
	}
}

// ── Instalação em goroutine ───────────────────────────────────────────────────
func runInstall(hwnd uintptr) {
	step := func(n int) {
		_PostMessageW.Call(hwnd, WM_APP_STEP, uintptr(n), 0)
	}
	status := func(s string, pct int) {
		logf("worker  %s", s)
		setSharedMsg(&statusMsg, s)
		_PostMessageW.Call(hwnd, WM_APP_STATUS, 0, 0)
		_PostMessageW.Call(hwnd, WM_APP_PROGRESS, uintptr(pct), 0)
	}
	fail := func(s string) {
		logf("worker  ERRO: %s", s)
		setSharedMsg(&errorMsg, s)
		_PostMessageW.Call(hwnd, WM_APP_ERROR, 0, 0)
	}

	// Etapa 1 — parar processos
	step(1)
	status("Parando processos existentes...", 5)
	stopRustDeskProcesses()

	// Etapa 2 — instalar RustDesk (cliente com marca, se disponivel; senao oficial)
	step(2)
	setupExe := filepath.Join(os.TempDir(), "rustdesk-setup.exe")
	usedBranded := false
	if apiURL != "" && installCode != "" {
		status("Baixando cliente...", 10)
		if downloadBranded(setupExe, func(pct int) {
			status(fmt.Sprintf("Baixando cliente...  %d%%", pct), 10+pct/3)
		}) {
			usedBranded = true
		}
	}
	if !usedBranded {
		if _, err := os.Stat(rustdeskExe); err == nil {
			status("RustDesk ja instalado.", 44)
			setupExe = ""
		} else {
			status("Baixando RustDesk...", 10)
			if err := downloadWithProgress(rustdeskDownload, setupExe, func(pct int) {
				status(fmt.Sprintf("Baixando RustDesk...  %d%%", pct), 10+pct/3)
			}); err != nil {
				fail("Download falhou: " + err.Error())
				return
			}
		}
	}
	if setupExe != "" {
		status("Instalando RustDesk...", 44)
		// Timeout: sem isso, um install travado congela a interface pra sempre.
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		cmd := exec.CommandContext(ctx, setupExe, "--silent-install")
		cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
		err := cmd.Run()
		cancel()
		if ctx.Err() == context.DeadlineExceeded {
			fail("A instalacao demorou demais e foi cancelada. Feche o RustDesk se estiver aberto e tente novamente.")
			return
		}
		if err != nil {
			fail("Instalacao falhou: " + err.Error())
			return
		}
		stopRustDeskProcesses()
		rustdeskExe = resolveInstalledExe()
		// O RustDesk registra o servico e chama helpers pelo nome fixo
		// "RustDesk.exe". O build com marca renomeia o executavel, entao sem
		// esta copia o servico aponta para um arquivo inexistente e nao sobe.
		ensureCanonicalExe()
	}

	// O cliente Flutter depende do runtime VC++. Sem ele o executavel instalado
	// falha com 0xc0000135 (DLL nao encontrada).
	ensureVCRedist()

	// Etapa 3 — configurar
	step(3)
	status("Limpando configuração antiga...", 50)
	clearRustDeskConfigDirs()

	status("Aplicando configuração do servidor...", 55)
	if err := writeBaseConfig(); err != nil {
		fail("Erro ao salvar config: " + err.Error())
		return
	}
	if err := applyRustDeskOptions(); err != nil {
		// Tipicamente runtime ausente (0xc0000135): instala o VC++ e tenta de novo.
		status("Instalando componentes do Windows...", 60)
		installVCRedist()
		if err2 := applyRustDeskOptions(); err2 != nil {
			fail("Erro ao aplicar opções: " + err2.Error())
			return
		}
	}

	// Propagar as opcoes AGORA, com o servico ainda parado: e a unica janela em
	// que o RustDesk2.toml do usuario esta estavel. Com o servico no ar o cliente
	// reescreve esse arquivo e apaga as opcoes.
	status("Propagando configuração para o serviço...", 68)
	propagateConfigToSystemProfile()

	// Etapa 4 — serviço
	step(4)
	if agentEnabled == "true" {
		status("Instalando agente de gerenciamento...", 78)
		if err := installAgent(); err != nil {
			fail("Erro no agente: " + err.Error())
			return
		}
	}

	// O servico sobe ANTES da senha: o "--password" fala com ele por IPC e, com
	// o servico parado, sai com codigo 0 sem gravar nada. Era esse o motivo de o
	// RustDesk.toml nunca existir na hora de propagar.
	status("Ativando serviço de inicialização...", 84)
	installRustDeskService()
	if !waitForServiceRunning(30 * time.Second) {
		logf("worker  AVISO: serviço não entrou em RUNNING em 30s")
	}

	if unattendedPassword != "" {
		status("Definindo senha de acesso remoto...", 89)
		if !setRustDeskPasswordWithRetry(unattendedPassword) {
			fail("O cliente não gravou a senha de acesso. Veja %TEMP%\\rustdesk-install.log")
			return
		}
		// So o RustDesk.toml: a propagacao completa faz RemoveAll no destino e
		// apagaria o RustDesk2.toml que o servico ja tem com as opcoes.
		status("Propagando senha para o serviço...", 92)
		copyConfigFile("RustDesk.toml")
	}

	status("Reiniciando serviço...", 94)
	restartRustDeskService()

	// Conferir depois do servico subir: e ele que reescreve o config do usuario.
	// Antes de abrir o cliente, para nao brigar com o arquivo.
	status("Verificando configuração...", 95)
	if !ensureFinalConfig(unattendedPassword) {
		fail("A configuração não pôde ser aplicada. Veja %TEMP%\\rustdesk-install.log")
		return
	}

	status("Iniciando RustDesk...", 97)
	exec.Command(rustdeskExe).Start()

	status("Instalação concluída!", 100)
	_PostMessageW.Call(hwnd, WM_APP_DONE, 0, 0)
}

// ── Config: caminhos e verificacao ────────────────────────────────────────────

func userConfigDir() string {
	appData, err := os.UserConfigDir()
	if err != nil {
		return ""
	}
	return filepath.Join(appData, "RustDesk", "config")
}

// waitForConfigKeys aguarda as chaves aparecerem de fato no arquivo.
//
// Os comandos do cliente (--password, --option) saem com codigo 0 antes de a
// escrita chegar ao disco. Confiar no codigo de saida foi o que deixou o
// servico sem RustDesk.toml: a propagacao rodou 120ms depois do --password e
// copiou uma pasta onde o arquivo ainda nao existia.
func waitForConfigKeys(path string, keys []string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for {
		if data, err := os.ReadFile(path); err == nil {
			missing := false
			for _, k := range keys {
				if !strings.Contains(string(data), k) {
					missing = true
					break
				}
			}
			if !missing {
				return true
			}
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(250 * time.Millisecond)
	}
}

// configComplete diz se a pasta tem tudo que o RustDesk precisa para aceitar
// conexao nao atendida: servidor/chave no RustDesk2.toml e, quando ha senha
// configurada, o password/salt que o proprio cliente grava no RustDesk.toml.
func configComplete(dir string) bool {
	if !waitForConfigKeys(filepath.Join(dir, "RustDesk2.toml"),
		[]string{"custom-rendezvous-server =", "key ="}, 0) {
		return false
	}
	if unattendedPassword != "" &&
		!waitForConfigKeys(filepath.Join(dir, "RustDesk.toml"),
			[]string{"password =", "salt ="}, 0) {
		return false
	}
	return true
}

// waitForServiceRunning aguarda o servico entrar em RUNNING.
//
// O "--password" do cliente conversa com o servico: com ele parado, o comando
// sai com codigo 0 e nao grava nada. Era esse o bug — a senha era definida na
// etapa 3 e o servico so subia na etapa 4.
func waitForServiceRunning(timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for {
		cmd := exec.Command("sc", "query", "RustDesk")
		cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
		out, _ := cmd.CombinedOutput()
		if strings.Contains(string(out), "RUNNING") {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(500 * time.Millisecond)
	}
}

// writeBaseConfig grava o RustDesk2.toml do usuario com servidor, chave e API.
//
// Sem permanent-password de proposito: o cliente guarda esse campo em formato
// interno criptografado, e um valor escrito a mao em texto puro e rejeitado na
// conexao. Quem grava e o "--password"; assim a presenca do campo vira prova de
// que o cliente realmente gravou.
func writeBaseConfig() error {
	configDir := userConfigDir()
	if configDir == "" {
		return fmt.Errorf("não foi possível localizar a pasta de configuração")
	}
	if err := os.MkdirAll(configDir, 0755); err != nil {
		return err
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "rendezvous_server = '%s:21116'\n", serverIP)
	sb.WriteString("nat_type = 1\nserial = 0\n\n[options]\n")
	fmt.Fprintf(&sb, "key = '%s'\n", serverKey)
	fmt.Fprintf(&sb, "custom-rendezvous-server = '%s'\n", serverIP)
	fmt.Fprintf(&sb, "relay-server = '%s'\n", serverIP)
	effectiveAPIURL := apiURL
	if apiURL != "" && tenantID != "" {
		effectiveAPIURL = strings.TrimRight(apiURL, "/") + "/t/" + tenantID
	}
	if effectiveAPIURL != "" {
		fmt.Fprintf(&sb, "api-server = '%s'\n", effectiveAPIURL)
	}
	return os.WriteFile(filepath.Join(configDir, "RustDesk2.toml"), []byte(sb.String()), 0644)
}

// ── Helpers de opções ─────────────────────────────────────────────────────────
func applyRustDeskOptions() error {
	options := [][2]string{
		{"key", serverKey},
		{"custom-rendezvous-server", serverIP},
		{"relay-server", serverIP},
	}
	if apiURL != "" {
		effectiveAPIURL := strings.TrimRight(apiURL, "/") + "/t/" + tenantID
		options = append(options, [2]string{"api-server", effectiveAPIURL})
	}
	for _, opt := range options {
		cmd := exec.Command(rustdeskExe, "--option", opt[0], opt[1])
		cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("%s: %v: %s", opt[0], err, strings.TrimSpace(string(out)))
		}
	}
	// Conferir no disco: codigo 0 nao garante que a opcao foi gravada.
	cfg := filepath.Join(userConfigDir(), "RustDesk2.toml")
	keys := make([]string, 0, len(options))
	for _, opt := range options {
		keys = append(keys, opt[0]+" =")
	}
	if !waitForConfigKeys(cfg, keys, 15*time.Second) {
		return fmt.Errorf("opções não apareceram em %s", cfg)
	}
	logf("worker  opções confirmadas em %s", cfg)
	return nil
}

func clearRustDeskConfigDirs() {
	appData, err := os.UserConfigDir()
	if err == nil {
		userCfg := filepath.Join(appData, "RustDesk", "config")
		os.RemoveAll(userCfg)
		os.MkdirAll(userCfg, 0755)
	}
	for _, dir := range []string{
		`C:\Windows\System32\config\systemprofile\AppData\Roaming\RustDesk\config`,
		`C:\Windows\SysWOW64\config\systemprofile\AppData\Roaming\RustDesk\config`,
	} {
		os.RemoveAll(dir)
	}
}

func stopRustDeskProcesses() {
	svcStop := exec.Command("sc", "stop", "RustDesk")
	svcStop.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	svcStop.Run()
	kill := exec.Command("taskkill", "/F", "/IM", "rustdesk.exe")
	// O cliente com marca tem o nome do app (ex.: "Acme Remoto.exe"), nao
	// "rustdesk.exe". Mata qualquer executavel da pasta de instalacao, senao o
	// processo antigo segura os arquivos e o --silent-install trava.
	if entries, err := os.ReadDir(filepath.Dir(rustdeskExe)); err == nil {
		for _, e := range entries {
			if e.IsDir() || !strings.EqualFold(filepath.Ext(e.Name()), ".exe") {
				continue
			}
			k := exec.Command("taskkill", "/F", "/IM", e.Name())
			k.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
			_ = k.Run()
		}
	}
	kill.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	kill.Run()
	time.Sleep(1500 * time.Millisecond)
}

func propagateConfigToSystemProfile() {
	appData, err := os.UserConfigDir()
	if err != nil {
		return
	}
	srcDir := filepath.Join(appData, "RustDesk", "config")
	entries, err := os.ReadDir(srcDir)
	if err != nil {
		logf("worker  propagacao abortada, %s ilegivel: %v", srcDir, err)
		return
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() {
			names = append(names, e.Name())
		}
	}

	// A propagacao apaga o destino antes de copiar. Se a origem estiver
	// incompleta, isso destroi uma config de servico que estava correta — foi
	// o que aconteceu quando o reparo rodou com a pasta do usuario ja limpa
	// pelo servico. Melhor nao propagar do que propagar pior.
	if !configComplete(srcDir) {
		logf("worker  propagacao cancelada: origem incompleta (%s)", strings.Join(names, ", "))
		return
	}
	logf("worker  propagando %d arquivo(s): %s", len(names), strings.Join(names, ", "))
	dsts := []string{
		`C:\Windows\System32\config\systemprofile\AppData\Roaming\RustDesk\config`,
		`C:\Windows\SysWOW64\config\systemprofile\AppData\Roaming\RustDesk\config`,
	}
	for _, dst := range dsts {
		os.RemoveAll(dst)
		if err := os.MkdirAll(dst, 0755); err != nil {
			continue
		}
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			data, err := os.ReadFile(filepath.Join(srcDir, e.Name()))
			if err != nil {
				continue
			}
			os.WriteFile(filepath.Join(dst, e.Name()), data, 0644)
		}
	}
}

// systemProfileConfigDirs sao as pastas lidas pelo servico, que roda como SYSTEM.
var systemProfileConfigDirs = []string{
	`C:\Windows\System32\config\systemprofile\AppData\Roaming\RustDesk\config`,
	`C:\Windows\SysWOW64\config\systemprofile\AppData\Roaming\RustDesk\config`,
}

// copyConfigFile copia um unico arquivo da pasta do usuario para o systemprofile,
// sem apagar o resto. A propagacao completa faz RemoveAll no destino, o que
// destroi a config que o servico ja tem quando a origem esta incompleta.
func copyConfigFile(name string) bool {
	data, err := os.ReadFile(filepath.Join(userConfigDir(), name))
	if err != nil {
		logf("worker  %s ilegivel: %v", name, err)
		return false
	}
	ok := false
	for _, dst := range systemProfileConfigDirs {
		if err := os.MkdirAll(dst, 0755); err != nil {
			continue
		}
		if err := os.WriteFile(filepath.Join(dst, name), data, 0644); err == nil {
			ok = true
		}
	}
	logf("worker  %s copiado para o systemprofile: %v", name, ok)
	return ok
}

// ensureFinalConfig confere o estado que realmente importa e refaz se preciso.
//
// Subir o servico faz o cliente reescrever o RustDesk2.toml do usuario e apagar
// as opcoes do instalador. Como isso depende de corrida, nao basta reordenar: e
// preciso conferir o resultado e corrigir.
func ensureFinalConfig(password string) bool {
	okUser := configComplete(userConfigDir())
	okSvc := configComplete(systemProfileConfigDirs[0])
	logf("worker  config: usuario=%v servico=%v", okUser, okSvc)

	// Quem decide a conexao nao atendida e a config do servico, que roda como
	// SYSTEM. A do usuario e reescrita pelo cliente toda vez que o servico sobe;
	// insistir nela era o que fazia o reparo rodar em circulos.
	if okSvc {
		if !okUser {
			logf("worker  config do usuario incompleta (o cliente reescreveu); serviço OK, seguindo")
		}
		return true
	}

	logf("worker  serviço sem config valida — refazendo")

	// Mesma sequencia do fluxo principal: opcoes com o servico parado (unica
	// janela em que o RustDesk2.toml fica estavel), senha com ele no ar.
	stopRustDeskProcesses()
	if err := writeBaseConfig(); err != nil {
		logf("worker  reescrever config base falhou: %v", err)
		return false
	}
	if err := applyRustDeskOptions(); err != nil {
		logf("worker  reaplicar opções falhou: %v", err)
		return false
	}
	// A guarda interna recusa origem incompleta, para nao apagar um destino bom.
	propagateConfigToSystemProfile()

	installRustDeskService()
	if !waitForServiceRunning(30 * time.Second) {
		logf("worker  serviço não subiu para regravar a senha")
		return false
	}
	if password != "" {
		if !setRustDeskPasswordWithRetry(password) {
			logf("worker  cliente nao gravou a senha")
			return false
		}
		copyConfigFile("RustDesk.toml")
	}
	restartRustDeskService()

	okSvc = configComplete(systemProfileConfigDirs[0])
	logf("worker  serviço apos correcao: %v", okSvc)
	return okSvc
}

func setRustDeskPasswordWithRetry(password string) bool {
	// A senha permanente e o "password" (criptografado) do RustDesk.toml, junto
	// com o "salt" — foi o que o teste manual mostrou apos subir o servico.
	cfg := filepath.Join(userConfigDir(), "RustDesk.toml")
	for i := 0; i < 6; i++ {
		cmd := exec.Command(rustdeskExe, "--password", password)
		cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
		out, err := cmd.CombinedOutput()
		if err != nil {
			logf("worker  --password tentativa %d falhou: %v: %s",
				i+1, err, strings.TrimSpace(string(out)))
			time.Sleep(2 * time.Second)
			continue
		}
		if waitForConfigKeys(cfg, []string{"password =", "salt ="}, 15*time.Second) {
			logf("worker  senha confirmada em %s (tentativa %d)", cfg, i+1)
			return true
		}
		// Sai 0 sem gravar quando o servico nao esta rodando.
		logf("worker  --password saiu 0 sem gravar (tentativa %d); serviço RUNNING=%v",
			i+1, waitForServiceRunning(0))
		time.Sleep(2 * time.Second)
	}
	return false
}

func installRustDeskService() {
	svcInstall := exec.Command(rustdeskExe, "--install-service")
	svcInstall.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	svcInstall.Run()
	scConfig := exec.Command("sc", "config", "RustDesk", "start=", "auto")
	scConfig.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	scConfig.Run()
	scStart := exec.Command("sc", "start", "RustDesk")
	scStart.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	scStart.Run()
}

func installAgent() error {
	if len(embeddedAgent) == 0 {
		return fmt.Errorf("agente não incluído no instalador")
	}
	if err := os.MkdirAll(agentDir, 0755); err != nil {
		return err
	}
	exec.Command("schtasks", "/End", "/TN", agentTask).Run()
	exec.Command("taskkill", "/F", "/IM", "rustdesk-agent.exe").Run()
	if err := os.WriteFile(agentExe, embeddedAgent, 0755); err != nil {
		return err
	}
	create := exec.Command(
		"schtasks", "/Create",
		"/TN", agentTask,
		"/TR", fmt.Sprintf(`"%s"`, agentExe),
		"/SC", "ONSTART",
		"/RU", "SYSTEM",
		"/RL", "HIGHEST",
		"/F",
	)
	create.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	if out, err := create.CombinedOutput(); err != nil {
		return fmt.Errorf("%v: %s", err, strings.TrimSpace(string(out)))
	}
	start := exec.Command("schtasks", "/Run", "/TN", agentTask)
	start.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	start.Run()
	return nil
}

// ── Helpers Win32 ─────────────────────────────────────────────────────────────
func createFont(size int, bold bool) uintptr {
	weight := 400
	if bold {
		weight = 600
	}
	face, _ := windows.UTF16PtrFromString("Segoe UI")
	h, _, _ := _CreateFontW.Call(
		uintptr(size), 0, 0, 0,
		uintptr(weight),
		0, 0, 0, 1, 0, 0, 4, 0,
		uintptr(unsafe.Pointer(face)),
	)
	return h
}

// ── Log de diagnostico ────────────────────────────────────────────────────────
// Grava em %TEMP%\rustdesk-install.log. Numa maquina remota e a unica forma de
// saber onde parou sem depender de deducao.
var (
	logMu   sync.Mutex
	logFile *os.File
)

func logf(format string, args ...any) {
	logMu.Lock()
	defer logMu.Unlock()
	if logFile == nil {
		f, err := os.OpenFile(
			filepath.Join(os.TempDir(), "rustdesk-install.log"),
			os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
		if err != nil {
			return
		}
		// BOM: sem ele o Get-Content do PowerShell 5.1 le como ANSI e quebra os acentos.
		if st, serr := f.Stat(); serr == nil && st.Size() == 0 {
			f.Write([]byte{0xEF, 0xBB, 0xBF})
		}
		logFile = f
	}
	fmt.Fprintf(logFile, "%s  %s\n",
		time.Now().Format("15:04:05.000"), fmt.Sprintf(format, args...))
	logFile.Sync() // travamento = processo morto sem flush; sem Sync o log some
}

func setWinText(hwnd uintptr, s string) {
	p, _ := windows.UTF16PtrFromString(s)
	_SetWindowTextW.Call(hwnd, uintptr(unsafe.Pointer(p)))
}

func msgBox(owner uintptr, text, title string, flags uint32) {
	t, _ := windows.UTF16PtrFromString(text)
	tt, _ := windows.UTF16PtrFromString(title)
	_MessageBoxW.Call(owner, uintptr(unsafe.Pointer(t)), uintptr(unsafe.Pointer(tt)), uintptr(flags))
}

func relaunchAsAdmin() {
	exe, _ := os.Executable()
	verb, _ := windows.UTF16PtrFromString("runas")
	file, _ := windows.UTF16PtrFromString(exe)
	type shellExInfo struct {
		cbSize         uint32
		fMask          uint32
		hwnd           uintptr
		lpVerb         *uint16
		lpFile         *uint16
		lpParameters   *uint16
		lpDirectory    *uint16
		nShow          int32
		hInstApp       uintptr
		lpIDList       uintptr
		lpClass        *uint16
		hkeyClass      uintptr
		dwHotKey       uint32
		hIconOrMonitor uintptr
		hProcess       uintptr
	}
	info := shellExInfo{fMask: 0x00000040, lpVerb: verb, lpFile: file, nShow: SW_SHOW}
	info.cbSize = uint32(unsafe.Sizeof(info))
	_ShellExecuteW.Call(0,
		uintptr(unsafe.Pointer(verb)),
		uintptr(unsafe.Pointer(file)),
		0, 0, SW_SHOW,
	)
}

// stallTimeout aborta o download se nenhum byte chegar nesse intervalo. Sem
// isso, uma conexao pendurada congela a instalacao para sempre.
const stallTimeout = 90 * time.Second

var httpClient = &http.Client{
	Transport: &http.Transport{
		DialContext:           (&net.Dialer{Timeout: 20 * time.Second}).DialContext,
		TLSHandshakeTimeout:   20 * time.Second,
		ResponseHeaderTimeout: 60 * time.Second,
	},
}

func downloadWithProgress(url, dest string, progress func(int)) error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("servidor respondeu %s", resp.Status)
	}

	f, err := os.Create(dest)
	if err != nil {
		return err
	}
	defer f.Close()

	// Watchdog: cancela o request se o stream parar de entregar bytes.
	var lastRead atomic.Int64
	lastRead.Store(time.Now().UnixNano())
	stopWatch := make(chan struct{})
	defer close(stopWatch)
	go func() {
		t := time.NewTicker(10 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-stopWatch:
				return
			case <-t.C:
				if time.Since(time.Unix(0, lastRead.Load())) > stallTimeout {
					cancel()
					return
				}
			}
		}
	}()

	total := resp.ContentLength
	var done int64
	lastPct := -1
	buf := make([]byte, 128*1024)
	for {
		n, err := resp.Body.Read(buf)
		if n > 0 {
			lastRead.Store(time.Now().UnixNano())
			if _, werr := f.Write(buf[:n]); werr != nil {
				return werr
			}
			done += int64(n)
			// So notifica quando o percentual inteiro muda: cada notificacao
			// repinta a janela toda, e um repaint por chunk trava a UI.
			if total > 0 {
				if pct := int(float64(done) / float64(total) * 100); pct != lastPct {
					lastPct = pct
					progress(pct)
				}
			}
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			if ctx.Err() != nil {
				return fmt.Errorf("download travou (sem dados por %s)", stallTimeout)
			}
			return err
		}
	}
	// Download truncado passaria despercebido e instalaria um exe corrompido.
	if total > 0 && done != total {
		return fmt.Errorf("download incompleto: %d de %d bytes", done, total)
	}
	return f.Sync()
}

// downloadBranded tenta baixar o cliente com marca do tenant. true se OK.
func downloadBranded(dest string, progress func(int)) bool {
	url := strings.TrimRight(apiURL, "/") + "/api/branded/" + installCode
	if err := downloadWithProgress(url, dest, progress); err != nil {
		os.Remove(dest) // nao deixar arquivo parcial para tras
		return false
	}
	if fi, err := os.Stat(dest); err != nil || fi.Size() < 1024*1024 {
		os.Remove(dest)
		return false // muito pequeno = provavelmente erro/404, nao um exe
	}
	return true
}

// resolveInstalledExe localiza o executavel instalado.
// O cliente com marca mantem a pasta (C:\Program Files\RustDesk), o servico
// (RustDesk) e a config (%APPDATA%\RustDesk) — mas o .exe e renomeado para
// "<Nome do App>.exe". Por isso pegamos o maior .exe da pasta, ignorando helpers.
func resolveInstalledExe() string {
	if _, err := os.Stat(rustdeskExe); err == nil {
		return rustdeskExe
	}
	if best := mainExeIn(filepath.Dir(rustdeskExe)); best != "" {
		return best
	}
	// Fallback: pastas em Program Files cujo nome lembre rustdesk.
	for _, base := range []string{os.Getenv("ProgramFiles"), `C:\Program Files`} {
		if base == "" {
			continue
		}
		entries, err := os.ReadDir(base)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if !e.IsDir() || !strings.Contains(strings.ToLower(e.Name()), "rustdesk") {
				continue
			}
			if best := mainExeIn(filepath.Join(base, e.Name())); best != "" {
				return best
			}
		}
	}
	return rustdeskExe
}

// mainExeIn devolve o maior .exe da pasta, ignorando helpers conhecidos.
func mainExeIn(dir string) string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return ""
	}
	best, bestSize := "", int64(0)
	for _, e := range entries {
		if e.IsDir() || !strings.EqualFold(filepath.Ext(e.Name()), ".exe") {
			continue
		}
		low := strings.ToLower(e.Name())
		if strings.HasPrefix(low, "runtimebroker") || strings.Contains(low, "deviceinstaller") || strings.Contains(low, "usbmmidd") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		if info.Size() > bestSize {
			best, bestSize = filepath.Join(dir, e.Name()), info.Size()
		}
	}
	return best
}

// vcRedistInstalled indica se o runtime VC++ x64 ja esta presente no sistema.
func vcRedistInstalled() bool {
	root := os.Getenv("SystemRoot")
	if root == "" {
		root = `C:\Windows`
	}
	for _, dll := range []string{"vcruntime140.dll", "msvcp140.dll"} {
		if _, err := os.Stat(filepath.Join(root, "System32", dll)); err != nil {
			return false
		}
	}
	return true
}

// ensureVCRedist instala o VC++ Redistributable se ainda nao estiver presente.
func ensureVCRedist() {
	if vcRedistInstalled() {
		return
	}
	installVCRedist()
}

// installVCRedist baixa e instala o VC++ Redistributable x64 silenciosamente.
func installVCRedist() {
	tmp := filepath.Join(os.TempDir(), "vc_redist.x64.exe")
	if err := downloadWithProgress("https://aka.ms/vs/17/release/vc_redist.x64.exe", tmp, func(int) {}); err != nil {
		return
	}
	cmd := exec.Command(tmp, "/install", "/quiet", "/norestart")
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	_ = cmd.Run()
}

// ensureCanonicalExe garante que exista um "RustDesk.exe" na pasta de instalacao.
// O cliente com marca tem o executavel renomeado, mas o RustDesk registra o
// servico como "RustDesk.exe" — sem essa copia o servico nao inicia (erro 2).
func ensureCanonicalExe() {
	if rustdeskExe == "" {
		return
	}
	dir := filepath.Dir(rustdeskExe)
	canonical := filepath.Join(dir, "RustDesk.exe")
	if strings.EqualFold(filepath.Base(rustdeskExe), "RustDesk.exe") {
		return
	}
	// Se ja existe e tem o mesmo tamanho, nao precisa recopiar.
	src, err := os.Stat(rustdeskExe)
	if err != nil {
		return
	}
	if dst, err := os.Stat(canonical); err == nil && dst.Size() == src.Size() {
		return
	}
	data, err := os.ReadFile(rustdeskExe)
	if err != nil {
		return
	}
	_ = os.WriteFile(canonical, data, 0o755)
}

// restartRustDeskService reinicia o servico para que ele releia a config final
// (servidor, key, api-server e senha) gravada durante a instalacao.
func restartRustDeskService() {
	stop := exec.Command("sc", "stop", "RustDesk")
	stop.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	_ = stop.Run()
	time.Sleep(3 * time.Second)
	start := exec.Command("sc", "start", "RustDesk")
	start.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	_ = start.Run()
}
