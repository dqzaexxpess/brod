package killswitch

import (
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"os"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// WFP — киллсвитч Windows поверх Windows Filtering Platform (набор фильтров — wfp_spec.go).
//
// Фильтры ставятся в обычной (не динамической) сессии и без флага PERSISTENT: они переживают падение демона (трафик мимо туннеля остаётся
// заблокированным, пока служба не поднимется или правила не снимут), но исчезают при перезагрузке — как таблица nftables на Linux.
// Провайдер, подуровень и ключи фильтров фиксированные, поэтому свой набор всегда можно найти и снять, не трогая чужие объекты:
//
//	brodctl killswitch release      (консоль администратора)
//
// Ключи фильтров — два банка по wfpMaxFilters: новый набор ставится в свободный банк, а прежний удаляется в той же транзакции, так что
// замена (например, после пересоздания TUN с новым LUID) проходит без окна, когда фильтров нет.
type WFP struct {
	AppPath string // исполняемый файл движка; пусто — текущий процесс
	// Dynamic — динамическая сессия: фильтры живут, пока открыта сессия этого процесса (Close или выход процесса снимает их гарантированно).
	// Для проверки набора на живой машине; у демона — false.
	Dynamic bool

	mu      sync.Mutex
	session uintptr // открытая динамическая сессия
}

// Фиксированные ключи. Динамический (проверочный) набор использует свои, чтобы не пересекаться с рабочим.
var (
	wfpProviderKey = windows.GUID{Data1: 0x62726f64, Data2: 0x6b73, Data3: 0x4f00, Data4: [8]byte{0x9a, 0x3c, 0x5d, 0x1e, 0x0f, 0x7a, 0x2b, 0x10}}
	wfpSublayerKey = windows.GUID{Data1: 0x62726f64, Data2: 0x6b73, Data3: 0x4f01, Data4: [8]byte{0x9a, 0x3c, 0x5d, 0x1e, 0x0f, 0x7a, 0x2b, 0x10}}
	wfpFilterBase  = windows.GUID{Data1: 0x62726f64, Data2: 0x6b73, Data3: 0x4f02, Data4: [8]byte{0x9a, 0x3c, 0x5d, 0x1e, 0x0f, 0x00, 0x00, 0x00}}
)

const wfpSublayerWeight = 0xfffe

type wfpKeys struct{ provider, sublayer, filterBase windows.GUID }

func (w *WFP) keys() wfpKeys {
	k := wfpKeys{wfpProviderKey, wfpSublayerKey, wfpFilterBase}
	if w.Dynamic {
		k.provider.Data3 |= 0x10
		k.sublayer.Data3 |= 0x10
		k.filterBase.Data4[5] = 1
	}
	return k
}

func (k wfpKeys) filter(bank, i int) windows.GUID {
	g := k.filterBase
	g.Data4[6], g.Data4[7] = byte(bank), byte(i)
	return g
}

// BindsInterface: набор привязан к LUID конкретного адаптера TUN, поэтому после каждого запуска ядра его ставят заново.
func (w *WFP) BindsInterface() bool { return true }

func (w *WFP) appPath() (string, error) {
	if w.AppPath != "" {
		return w.AppPath, nil
	}
	return os.Executable()
}

// Install ставит (или заменяет) набор. Если адаптера TUN ещё нет, ставится набор без его разрешения — кроме случая
// c.RequireInterface (набор после запуска ядра): там отсутствие адаптера — ошибка, иначе весь трафик туннеля был бы заблокирован при
// состоянии «киллсвитч включён».
func (w *WFP) Install(c Config) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	app, err := w.appPath()
	if err != nil {
		return err
	}
	luid, err := InterfaceLUID(c.Interface)
	if err != nil {
		return err
	}
	if luid == 0 && c.RequireInterface {
		return fmt.Errorf("интерфейс %s не найден", c.Interface)
	}
	rules, err := WFPRules(WFPConfig{AppPath: app, TunLUID: luid, AllowLAN: c.AllowLAN})
	if err != nil {
		return err
	}
	if !w.Dynamic {
		writeMarker(c) // до установки: при частичном сбое признак не должен отсутствовать
	}
	s, err := w.open()
	if err != nil {
		return err
	}
	defer w.release(s)
	if err := w.install(s, rules); err != nil {
		if !w.Dynamic {
			if any, perr := w.anyPresent(s); perr == nil && !any {
				removeMarker()
			}
		}
		return fmt.Errorf("не удалось поставить правила: %w", err)
	}
	return nil
}

func (w *WFP) install(s uintptr, rules []WFPRule) error {
	k := w.keys()
	var present [2][]windows.GUID
	for bank := range present {
		var err error
		if present[bank], err = presentFilters(s, k, bank); err != nil {
			return err
		}
	}
	bank := 0
	if len(present[0]) > 0 {
		bank = 1
	}
	if len(present[0]) > 0 && len(present[1]) > 0 { // оба банка заняты (сбой посреди замены): сначала освобождаем второй
		if err := transaction(s, func() error { return deleteFilters(s, present[1]) }); err != nil {
			return err
		}
		present[1] = nil
	}
	provider, sublayer, err := baseExists(s, k)
	if err != nil {
		return err
	}
	return transaction(s, func() error {
		if !provider {
			if err := addProvider(s, k); err != nil {
				return err
			}
		}
		if !sublayer {
			if err := addSublayer(s, k); err != nil {
				return err
			}
		}
		for i, r := range rules {
			if err := addFilter(s, k, k.filter(bank, i), r); err != nil {
				return fmt.Errorf("фильтр %d (%s %s): %w", i, r.Layer, r.Name, err)
			}
		}
		return deleteFilters(s, present[1-bank])
	})
}

// Remove снимает свой набор; если набора нет — ничего не делает. Динамическая сессия просто закрывается.
func (w *WFP) Remove() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.Dynamic {
		if w.session != 0 {
			fwpmEngineClose0(w.session)
			w.session = 0
		}
		return nil
	}
	had := Active()
	s, err := w.open()
	if err != nil {
		if !had {
			return nil // ставить было нечего (например, нет прав) — не ошибка при каждом запуске
		}
		return err
	}
	defer w.release(s)
	if err := removeAll(s, w.keys()); err != nil {
		if !had && errors.Is(err, windows.ERROR_ACCESS_DENIED) {
			return nil
		}
		return fmt.Errorf("не удалось снять правила: %w", err)
	}
	removeMarker()
	return nil
}

// Release снимает набор принудительно и всегда сообщает о неудаче (аварийная команда).
func (w *WFP) Release() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	s, err := w.open()
	if err != nil {
		return err
	}
	defer w.release(s)
	if err := removeAll(s, w.keys()); err != nil {
		return fmt.Errorf("не удалось снять правила: %w", err)
	}
	removeMarker()
	return nil
}

// Close закрывает динамическую сессию (её фильтры исчезают).
func (w *WFP) Close() error { return w.Remove() }

func removeAll(s uintptr, k wfpKeys) error {
	f0, err := presentFilters(s, k, 0)
	if err != nil {
		return err
	}
	f1, err := presentFilters(s, k, 1)
	if err != nil {
		return err
	}
	filters := append(f0, f1...)
	provider, sublayer, err := baseExists(s, k)
	if err != nil {
		return err
	}
	if len(filters) == 0 && !provider && !sublayer {
		return nil
	}
	return transaction(s, func() error {
		if err := deleteFilters(s, filters); err != nil {
			return err
		}
		if sublayer {
			if err := call("FwpmSubLayerDeleteByKey0", procFwpmSubLayerDeleteByKey0, s, uintptr(unsafe.Pointer(&k.sublayer))); err != nil {
				return err
			}
		}
		if provider {
			return call("FwpmProviderDeleteByKey0", procFwpmProviderDeleteByKey0, s, uintptr(unsafe.Pointer(&k.provider)))
		}
		return nil
	})
}

// Status перечисляет установленные фильтры: рабочий набор и проверочный (динамический, `brodctl killswitch test`).
func (w *WFP) Status() (string, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	s, err := w.open()
	if err != nil {
		return "", err
	}
	defer w.release(s)
	var b strings.Builder
	n := 0
	for _, set := range []struct {
		label string
		k     wfpKeys
	}{{"рабочий", (&WFP{}).keys()}, {"проверочный", (&WFP{Dynamic: true}).keys()}} {
		for bank := 0; bank < 2; bank++ {
			for i := 0; i < wfpMaxFilters; i++ {
				key := set.k.filter(bank, i)
				var f *wtFwpmFilter0
				r, _, _ := procFwpmFilterGetByKey0.Call(s, uintptr(unsafe.Pointer(&key)), uintptr(unsafe.Pointer(&f)))
				if wfpFailed(r) {
					if !wfpNotFound(r) {
						return "", fmt.Errorf("FwpmFilterGetByKey0: 0x%08x (%v)", uint32(r), syscall.Errno(r))
					}
					continue
				}
				fmt.Fprintf(&b, "%s %d/%02d id=%d %s\n", set.label, bank, i, f.filterID, windows.UTF16PtrToString(f.displayData.name))
				procFwpmFreeMemory0.Call(uintptr(unsafe.Pointer(&f)))
				n++
			}
		}
	}
	if n == 0 {
		return "правил киллсвитча нет\n", nil
	}
	return fmt.Sprintf("фильтров: %d\n%s", n, b.String()), nil
}

// Probe проверяет, что движок WFP открывается (ничего не меняя).
func (w *WFP) Probe() (string, error) {
	s, err := openEngine(false)
	if err != nil {
		return "", err
	}
	fwpmEngineClose0(s)
	return "WFP доступен", nil
}

func (w *WFP) anyPresent(s uintptr) (bool, error) {
	k := w.keys()
	for bank := 0; bank < 2; bank++ {
		f, err := presentFilters(s, k, bank)
		if err != nil || len(f) > 0 {
			return len(f) > 0, err
		}
	}
	return false, nil
}

// open: для динамического режима — сессия, которая живёт до Remove/Close; иначе — новая на один вызов.
func (w *WFP) open() (uintptr, error) {
	if !w.Dynamic {
		return openEngine(false)
	}
	if w.session == 0 {
		s, err := openEngine(true)
		if err != nil {
			return 0, err
		}
		w.session = s
	}
	return w.session, nil
}

func (w *WFP) release(s uintptr) {
	if !w.Dynamic {
		fwpmEngineClose0(s)
	}
}

func presentFilters(s uintptr, k wfpKeys, bank int) ([]windows.GUID, error) {
	var out []windows.GUID
	for i := 0; i < wfpMaxFilters; i++ {
		key := k.filter(bank, i)
		ok, err := exists(procFwpmFilterGetByKey0, s, &key)
		if err != nil {
			return nil, err
		}
		if ok {
			out = append(out, key)
		}
	}
	return out, nil
}

func baseExists(s uintptr, k wfpKeys) (provider, sublayer bool, err error) {
	if provider, err = exists(procFwpmProviderGetByKey0, s, &k.provider); err != nil {
		return
	}
	sublayer, err = exists(procFwpmSubLayerGetByKey0, s, &k.sublayer)
	return
}

// InterfaceLUID — LUID адаптера по имени; 0 без ошибки, если адаптера нет.
func InterfaceLUID(name string) (uint64, error) {
	if name == "" {
		return 0, nil
	}
	ifc, err := net.InterfaceByName(name)
	if err != nil {
		return 0, nil
	}
	row := windows.MibIfRow2{InterfaceIndex: uint32(ifc.Index)}
	if err := windows.GetIfEntry2Ex(windows.MibIfEntryNormalWithoutStatistics, &row); err != nil {
		return 0, fmt.Errorf("LUID интерфейса %s: %w", name, err)
	}
	return row.InterfaceLuid, nil
}

// --- вызовы WFP ---

var (
	modFwpuclnt                   = windows.NewLazySystemDLL("fwpuclnt.dll")
	procFwpmEngineOpen0           = modFwpuclnt.NewProc("FwpmEngineOpen0")
	procFwpmEngineClose0          = modFwpuclnt.NewProc("FwpmEngineClose0")
	procFwpmTransactionBegin0     = modFwpuclnt.NewProc("FwpmTransactionBegin0")
	procFwpmTransactionCommit0    = modFwpuclnt.NewProc("FwpmTransactionCommit0")
	procFwpmTransactionAbort0     = modFwpuclnt.NewProc("FwpmTransactionAbort0")
	procFwpmProviderAdd0          = modFwpuclnt.NewProc("FwpmProviderAdd0")
	procFwpmProviderGetByKey0     = modFwpuclnt.NewProc("FwpmProviderGetByKey0")
	procFwpmProviderDeleteByKey0  = modFwpuclnt.NewProc("FwpmProviderDeleteByKey0")
	procFwpmSubLayerAdd0          = modFwpuclnt.NewProc("FwpmSubLayerAdd0")
	procFwpmSubLayerGetByKey0     = modFwpuclnt.NewProc("FwpmSubLayerGetByKey0")
	procFwpmSubLayerDeleteByKey0  = modFwpuclnt.NewProc("FwpmSubLayerDeleteByKey0")
	procFwpmFilterAdd0            = modFwpuclnt.NewProc("FwpmFilterAdd0")
	procFwpmFilterGetByKey0       = modFwpuclnt.NewProc("FwpmFilterGetByKey0")
	procFwpmFilterDeleteByKey0    = modFwpuclnt.NewProc("FwpmFilterDeleteByKey0")
	procFwpmFreeMemory0           = modFwpuclnt.NewProc("FwpmFreeMemory0")
	procFwpmGetAppIdFromFileName0 = modFwpuclnt.NewProc("FwpmGetAppIdFromFileName0")
)

// call вызывает функцию WFP (они возвращают код ошибки, 0 — успех).
func call(name string, p *windows.LazyProc, args ...uintptr) error {
	r, _, _ := p.Call(args...)
	if !wfpFailed(r) {
		return nil
	}
	return fmt.Errorf("%s: 0x%08x (%w)", name, uint32(r), syscall.Errno(r))
}

func openEngine(dynamic bool) (uintptr, error) {
	name, _ := windows.UTF16PtrFromString("Brod")
	desc, _ := windows.UTF16PtrFromString("Brod killswitch")
	sess := wtFwpmSession0{displayData: wtFwpmDisplayData0{name: name, description: desc}, txnWaitTimeoutInMSec: windows.INFINITE}
	if dynamic {
		sess.flags = cFWPM_SESSION_FLAG_DYNAMIC
	}
	var h uintptr
	err := call("FwpmEngineOpen0", procFwpmEngineOpen0, 0, uintptr(cRPC_C_AUTHN_WINNT), 0, uintptr(unsafe.Pointer(&sess)), uintptr(unsafe.Pointer(&h)))
	runtime.KeepAlive(name)
	runtime.KeepAlive(desc)
	if err != nil {
		return 0, err
	}
	return h, nil
}

func fwpmEngineClose0(h uintptr) { procFwpmEngineClose0.Call(h) }

func transaction(s uintptr, f func() error) error {
	if err := call("FwpmTransactionBegin0", procFwpmTransactionBegin0, s, 0); err != nil {
		return err
	}
	if err := f(); err != nil {
		procFwpmTransactionAbort0.Call(s)
		return err
	}
	if err := call("FwpmTransactionCommit0", procFwpmTransactionCommit0, s); err != nil {
		procFwpmTransactionAbort0.Call(s)
		return err
	}
	return nil
}

// exists: объект с ключом есть (GetByKey успешен); «не найден» — false без ошибки, любой другой отказ (например, нет прав) — ошибка:
// аварийное снятие не должно сообщать «сняты», если оно просто не смогло посмотреть. Вызывается вне транзакции.
func exists(p *windows.LazyProc, s uintptr, key *windows.GUID) (bool, error) {
	var obj unsafe.Pointer
	r, _, _ := p.Call(s, uintptr(unsafe.Pointer(key)), uintptr(unsafe.Pointer(&obj)))
	if wfpFailed(r) {
		if wfpNotFound(r) {
			return false, nil
		}
		return false, fmt.Errorf("%s: 0x%08x (%w)", p.Name, uint32(r), syscall.Errno(r))
	}
	procFwpmFreeMemory0.Call(uintptr(unsafe.Pointer(&obj)))
	return true, nil
}

func deleteFilters(s uintptr, keys []windows.GUID) error {
	for i := range keys {
		if err := call("FwpmFilterDeleteByKey0", procFwpmFilterDeleteByKey0, s, uintptr(unsafe.Pointer(&keys[i]))); err != nil {
			return err
		}
	}
	return nil
}

func displayData(name, desc string) wtFwpmDisplayData0 {
	n, _ := windows.UTF16PtrFromString(name)
	d, _ := windows.UTF16PtrFromString(desc)
	return wtFwpmDisplayData0{name: n, description: d}
}

func addProvider(s uintptr, k wfpKeys) error {
	p := wtFwpmProvider0{providerKey: k.provider, displayData: displayData("Brod", "Brod VPN killswitch")}
	err := call("FwpmProviderAdd0", procFwpmProviderAdd0, s, uintptr(unsafe.Pointer(&p)), 0)
	runtime.KeepAlive(&p)
	return err
}

func addSublayer(s uintptr, k wfpKeys) error {
	sl := wtFwpmSublayer0{subLayerKey: k.sublayer, displayData: displayData("Brod killswitch", "Block traffic outside the Brod tunnel"),
		providerKey: &k.provider, weight: wfpSublayerWeight}
	err := call("FwpmSubLayerAdd0", procFwpmSubLayerAdd0, s, uintptr(unsafe.Pointer(&sl)), 0)
	runtime.KeepAlive(&sl)
	runtime.KeepAlive(&k)
	return err
}

var wfpLayerKeys = [...]*windows.GUID{
	LayerConnect4: &cFWPM_LAYER_ALE_AUTH_CONNECT_V4,
	LayerRecv4:    &cFWPM_LAYER_ALE_AUTH_RECV_ACCEPT_V4,
	LayerConnect6: &cFWPM_LAYER_ALE_AUTH_CONNECT_V6,
	LayerRecv6:    &cFWPM_LAYER_ALE_AUTH_RECV_ACCEPT_V6,
	LayerForward4: &cFWPM_LAYER_IPFORWARD_V4,
	LayerForward6: &cFWPM_LAYER_IPFORWARD_V6,
}

func addFilter(s uintptr, k wfpKeys, key windows.GUID, r WFPRule) error {
	var keep []any // значения условий, на которые указывают структуры: живы до конца вызова
	conds := make([]wtFwpmFilterCondition0, len(r.Conds))
	for i, c := range r.Conds {
		cond, held, err := wfpCondition(c)
		if err != nil {
			return err
		}
		conds[i] = cond
		keep = append(keep, held...)
	}
	f := wtFwpmFilter0{
		filterKey:   key,
		displayData: displayData("Brod: "+r.Name, ""),
		providerKey: &k.provider,
		layerKey:    *wfpLayerKeys[r.Layer],
		subLayerKey: k.sublayer,
		weight:      wtFwpValue0{_type: cFWP_UINT8, value: uintptr(r.Weight)},
		action:      wtFwpmAction0{_type: cFWP_ACTION_BLOCK},
	}
	if r.Permit {
		f.action._type = cFWP_ACTION_PERMIT
	}
	if r.Hard {
		f.flags = cFWPM_FILTER_FLAG_CLEAR_ACTION_RIGHT
	}
	if len(conds) > 0 {
		f.numFilterConditions = uint32(len(conds))
		f.filterCondition = &conds[0]
	}
	var id uint64
	err := call("FwpmFilterAdd0", procFwpmFilterAdd0, s, uintptr(unsafe.Pointer(&f)), 0, uintptr(unsafe.Pointer(&id)))
	runtime.KeepAlive(&f)
	runtime.KeepAlive(conds)
	runtime.KeepAlive(keep)
	runtime.KeepAlive(&k)
	for _, h := range keep {
		if b, ok := h.(appIDBlob); ok {
			procFwpmFreeMemory0.Call(uintptr(unsafe.Pointer(&b.p)))
		}
	}
	return err
}

// appIDBlob — app-id, выделенный WFP (освобождается FwpmFreeMemory0).
type appIDBlob struct{ p *wtFwpByteBlob }

func wfpCondition(c WFPCond) (wtFwpmFilterCondition0, []any, error) {
	cond := wtFwpmFilterCondition0{matchType: cFWP_MATCH_EQUAL}
	switch c.Kind {
	case CondAppID:
		path, err := windows.UTF16PtrFromString(c.Path)
		if err != nil {
			return cond, nil, err
		}
		var blob *wtFwpByteBlob
		if err := call("FwpmGetAppIdFromFileName0", procFwpmGetAppIdFromFileName0, uintptr(unsafe.Pointer(path)), uintptr(unsafe.Pointer(&blob))); err != nil {
			return cond, nil, fmt.Errorf("%s: %w", c.Path, err)
		}
		cond.fieldKey = cFWPM_CONDITION_ALE_APP_ID
		cond.conditionValue = wtFwpConditionValue0{_type: cFWP_BYTE_BLOB_TYPE, value: uintptr(unsafe.Pointer(blob))}
		return cond, []any{appIDBlob{blob}}, nil
	case CondLoopback:
		cond.fieldKey = cFWPM_CONDITION_FLAGS
		cond.matchType = cFWP_MATCH_FLAGS_ALL_SET
		cond.conditionValue = wtFwpConditionValue0{_type: cFWP_UINT32, value: uintptr(cFWP_CONDITION_FLAG_IS_LOOPBACK)}
	case CondLocalIface, CondForwardIface:
		cond.fieldKey = cFWPM_CONDITION_IP_LOCAL_INTERFACE
		if c.Kind == CondForwardIface {
			cond.fieldKey = cFWPM_CONDITION_IP_FORWARD_INTERFACE
		}
		v := new(uint64)
		*v = c.Num
		cond.conditionValue = wtFwpConditionValue0{_type: cFWP_UINT64, value: uintptr(unsafe.Pointer(v))}
		return cond, []any{v}, nil
	case CondProto:
		cond.fieldKey = cFWPM_CONDITION_IP_PROTOCOL
		cond.conditionValue = wtFwpConditionValue0{_type: cFWP_UINT8, value: uintptr(c.Num)}
	case CondLocalPort:
		cond.fieldKey = cFWPM_CONDITION_IP_LOCAL_PORT
		cond.conditionValue = wtFwpConditionValue0{_type: cFWP_UINT16, value: uintptr(c.Num)}
	case CondRemotePort:
		cond.fieldKey = cFWPM_CONDITION_IP_REMOTE_PORT
		cond.conditionValue = wtFwpConditionValue0{_type: cFWP_UINT16, value: uintptr(c.Num)}
	case CondRemoteAddr, CondLocalAddr, CondDestAddr:
		switch c.Kind {
		case CondRemoteAddr:
			cond.fieldKey = cFWPM_CONDITION_IP_REMOTE_ADDRESS
		case CondLocalAddr:
			cond.fieldKey = cFWPM_CONDITION_IP_LOCAL_ADDRESS
		default:
			cond.fieldKey = cFWPM_CONDITION_IP_DESTINATION_ADDRESS
		}
		p := c.Prefix.Masked()
		if p.Addr().Is4() {
			a4 := p.Addr().As4()
			v := &wtFwpV4AddrAndMask{addr: binary.BigEndian.Uint32(a4[:]), mask: ^uint32(0) << (32 - p.Bits())}
			if p.Bits() == 0 {
				v.mask = 0
			}
			cond.conditionValue = wtFwpConditionValue0{_type: cFWP_V4_ADDR_MASK, value: uintptr(unsafe.Pointer(v))}
			return cond, []any{v}, nil
		}
		v := &wtFwpV6AddrAndMask{addr: p.Addr().As16(), prefixLength: uint8(p.Bits())}
		cond.conditionValue = wtFwpConditionValue0{_type: cFWP_V6_ADDR_MASK, value: uintptr(unsafe.Pointer(v))}
		return cond, []any{v}, nil
	default:
		return cond, nil, fmt.Errorf("неизвестное условие %d", c.Kind)
	}
	return cond, nil, nil
}

// Ключи слоёв и условий, которых нет в скопированных типах (fwpmu.h, Windows SDK 10.0.22621).
var (
	cFWPM_LAYER_IPFORWARD_V4               = windows.GUID{Data1: 0xa82acc24, Data2: 0x4ee1, Data3: 0x4ee1, Data4: [8]byte{0xb4, 0x65, 0xfd, 0x1d, 0x25, 0xcb, 0x10, 0xa4}}
	cFWPM_LAYER_IPFORWARD_V6               = windows.GUID{Data1: 0x7b964818, Data2: 0x19c7, Data3: 0x493a, Data4: [8]byte{0xb7, 0x1f, 0x83, 0x2c, 0x36, 0x84, 0xd2, 0x8c}}
	cFWPM_CONDITION_IP_FORWARD_INTERFACE   = windows.GUID{Data1: 0x1076b8a5, Data2: 0x6323, Data3: 0x4c5e, Data4: [8]byte{0x98, 0x10, 0xe8, 0xd3, 0xfc, 0x9e, 0x61, 0x36}}
	cFWPM_CONDITION_IP_DESTINATION_ADDRESS = windows.GUID{Data1: 0x2d79133b, Data2: 0xb390, Data3: 0x45c6, Data4: [8]byte{0x86, 0x99, 0xac, 0xac, 0xea, 0xaf, 0xed, 0x33}}
)
