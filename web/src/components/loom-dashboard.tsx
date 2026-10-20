import {
  Activity as ActivityIcon,
  AlertTriangle,
  ArrowDownToLine,
  ArrowUpRight,
  Check,
  CircleHelp,
  Clipboard,
  Copy,
  FileClock,
  Folder,
  FolderSync,
  Globe2,
  Laptop,
  Link2,
  LoaderCircle,
  Network,
  Plus,
  Radio,
  RefreshCw,
  Server,
  Settings2,
  Trash2,
  Wifi,
  WifiOff,
  X,
  Zap,
  Ban,
  Unlock,
  Key,
  Shield,
} from "lucide-react"
import { Button } from "@/components/ui/button"
import {
  type FormEvent,
  type ReactNode,
  useCallback,
  useEffect,
  useId,
  useRef,
  useState,
} from "react"
import {
  type Activity,
  type AppState,
  type Conflict,
  type Diagnostics,
  type Device,
  type Folder as SharedFolder,
  type RelayInfo,
  type Service,
  type PairingSession,
  addRelay,
  deleteResource,
  getConflicts,
  getRelays,
  postJson,
  removeRelay,
  requestJson,
  resolveConflict,
  blockDevice,
  unblockDevice,
  generatePairingCode,
  claimPairingCode,
} from "@/lib/api"

type PageKey =
  "overview" | "devices" | "services" | "folders" | "activity" | "settings"
type DialogKey = "device" | "service" | "folder" | "diagnostics" | null

const navigation: { id: PageKey; label: string; icon: typeof Network }[] = [
  { id: "overview", label: "Overview", icon: Network },
  { id: "devices", label: "Devices", icon: Laptop },
  { id: "services", label: "Services", icon: Globe2 },
  { id: "folders", label: "Folders", icon: Folder },
  { id: "activity", label: "Activity", icon: ActivityIcon },
  { id: "settings", label: "Settings", icon: Settings2 },
]

const pageTitles: Record<PageKey, string> = {
  overview: "Overview",
  devices: "Devices",
  services: "Services",
  folders: "Folders",
  activity: "Activity",
  settings: "Settings",
}

export function LoomDashboard() {
  const [page, setPage] = useState<PageKey>("overview")
  const [dialog, setDialog] = useState<DialogKey>(null)
  const [conflictFolder, setConflictFolder] = useState<SharedFolder | null>(null)
  const [state, setState] = useState<AppState | null>(null)
  const [loading, setLoading] = useState(true)
  const [apiError, setApiError] = useState("")
  const [actionError, setActionError] = useState("")
  const [notice, setNotice] = useState("")
  const [busyKey, setBusyKey] = useState("")
  const [diagnostics, setDiagnostics] = useState<Diagnostics | null>(null)
  const [diagnosticsLoading, setDiagnosticsLoading] = useState(false)
  const [copied, setCopied] = useState(false)
  const [copiedAddress, setCopiedAddress] = useState("")
  const inFlight = useRef(false)

  const navigateTo = (nextPage: PageKey) => {
    setPage(nextPage)
    window.scrollTo({ top: 0, behavior: "auto" })
  }

  const refresh = useCallback(async (showLoading = false) => {
    if (inFlight.current) return
    inFlight.current = true
    if (showLoading) setLoading(true)
    try {
      const next = await requestJson<AppState>("/api/state", {
        cache: "no-store",
      })
      setState(next)
      setApiError("")
    } catch (error) {
      setApiError(
        error instanceof Error ? error.message : "Could not reach the Loom API"
      )
    } finally {
      setLoading(false)
      inFlight.current = false
    }
  }, [])

  useEffect(() => {
    void refresh(true)
    const poll = window.setInterval(() => void refresh(), 2000)
    return () => window.clearInterval(poll)
  }, [refresh])

  useEffect(() => {
    if (!notice) return
    const timeout = window.setTimeout(() => setNotice(""), 3600)
    return () => window.clearTimeout(timeout)
  }, [notice])

  const runAction = useCallback(
    async <T,>(
      key: string,
      action: () => Promise<T>,
      successMessage?: string
    ) => {
      setBusyKey(key)
      setActionError("")
      setNotice("")
      try {
        await action()
        await refresh()
        if (successMessage) setNotice(successMessage)
        return true
      } catch (error) {
        setActionError(
          error instanceof Error
            ? error.message
            : "The action could not be completed"
        )
        return false
      } finally {
        setBusyKey("")
      }
    },
    [refresh]
  )

  const handleAction = async (
    event: FormEvent<HTMLFormElement>,
    action: () => Promise<unknown>,
    message: string
  ) => {
    event.preventDefault()
    const success = await runAction("dialog", action, message)
    if (success) setDialog(null)
  }

  const openDiagnostics = async () => {
    setDialog("diagnostics")
    setActionError("")
    setDiagnostics(null)
    setDiagnosticsLoading(true)
    try {
      setDiagnostics(
        await requestJson<Diagnostics>("/api/diagnostics", {
          cache: "no-store",
        })
      )
    } catch (error) {
      setActionError(
        error instanceof Error
          ? error.message
          : "Diagnostics could not be loaded"
      )
    } finally {
      setDiagnosticsLoading(false)
    }
  }

  const copyAddress = async () => {
    const address = state?.network.addresses[0]
    if (!address) return
    await copyText(address)
  }

  const copyText = async (value: string) => {
    try {
      await navigator.clipboard.writeText(value)
      setCopied(true)
      setCopiedAddress(value)
      window.setTimeout(() => {
        setCopied(false)
        setCopiedAddress("")
      }, 1800)
    } catch {
      setActionError("Clipboard access is unavailable in this browser")
    }
  }

  if (loading && !state) return <LoadingView />
  if (!state) {
    return (
      <ApiUnavailable message={apiError} onRetry={() => void refresh(true)} />
    )
  }

  const openDialog = (next: DialogKey) => {
    setActionError("")
    setDialog(next)
  }

  return (
    <div className="app-shell">
      <aside className="sidebar">
        <a
          className="brand"
          href="#overview"
          onClick={(event) => {
            event.preventDefault()
            navigateTo("overview")
          }}
        >
          <span className="brand-mark">
            <Link2 size={19} strokeWidth={2.2} />
          </span>
          <span className="brand-name">loom</span>
        </a>

        <nav className="primary-nav" aria-label="Main navigation">
          {navigation.map(({ id, label, icon: Icon }) => (
            <button
              key={id}
              className={`nav-item ${page === id ? "is-active" : ""}`}
              onClick={() => navigateTo(id)}
              aria-current={page === id ? "page" : undefined}
            >
              <Icon size={17} strokeWidth={1.8} />
              <span>{label}</span>
            </button>
          ))}
        </nav>
      </aside>

      <main className="main-shell">
        <div className="content-area">
          <div className="page-heading">
            <div>
              <h1>{pageTitles[page]}</h1>
            </div>
            {state.network.demo && (
              <span className="page-demo-mark">
                <i /> Local demo
              </span>
            )}
            <div className="heading-actions">
              {page === "devices" || page === "overview" ? (
                <Button
                  type="button"
                  className="button button-primary"
                  onClick={() => openDialog("device")}
                >
                  <Plus size={16} /> Add device
                </Button>
              ) : page === "services" ? (
                <Button
                  type="button"
                  className="button button-primary"
                  onClick={() => openDialog("service")}
                >
                  <Plus size={16} /> Publish service
                </Button>
              ) : page === "folders" ? (
                <Button
                  type="button"
                  className="button button-primary"
                  onClick={() => openDialog("folder")}
                >
                  <Plus size={16} /> Add folder
                </Button>
              ) : null}
            </div>
          </div>

          {!state.network.connected && page !== "settings" && !apiError && (
            <div className="connection-warning" role="status">
              <WifiOff size={16} />
              <span>Networking paused</span>
              <button onClick={() => navigateTo("settings")}>
                Open Settings
              </button>
            </div>
          )}
          {apiError && (
            <div className="connection-warning" role="status">
              <WifiOff size={16} />
              <span>
                Can’t reach the Loom API. Showing the last received state.
              </span>
              <button onClick={() => void refresh()}>Retry</button>
            </div>
          )}
          {actionError && !dialog && (
            <div className="action-error" role="alert">
              <CircleHelp size={16} />
              <span>{actionError}</span>
              <button
                aria-label="Dismiss error"
                onClick={() => setActionError("")}
              >
                <X size={15} />
              </button>
            </div>
          )}
          {notice && (
            <div className="action-notice" role="status">
              <Check size={16} />
              <span>{notice}</span>
              <button
                aria-label="Dismiss message"
                onClick={() => setNotice("")}
              >
                <X size={15} />
              </button>
            </div>
          )}

          {page === "overview" && (
            <Overview
              state={state}
              busyKey={busyKey}
              onDevice={() => openDialog("device")}
              onService={() => openDialog("service")}
              onFolder={() => openDialog("folder")}
              onReviewConflicts={(folder) => setConflictFolder(folder)}
              onDemo={() =>
                void runAction(
                  "demo",
                  () => postJson("/api/demo/change", {}),
                  "Sample change synced"
                )
              }
              onPage={navigateTo}
              onSync={(folder) =>
                void runAction(
                  `sync-${folder.id}`,
                  () =>
                    postJson(
                      `/api/folders/${encodeURIComponent(folder.id)}/sync`,
                      {}
                    ),
                  `Sync request sent for ${folder.name}`
                )
              }
            />
          )}
          {page === "devices" && (
            <DevicesPage
              state={state}
              onAdd={() => openDialog("device")}
              onCopy={() => void copyAddress()}
              copied={copied}
              onBlockDevice={(device) => {
                if (
                  window.confirm(
                    `Block and revoke ${device.name}? All active connections will be terminated immediately.`
                  )
                ) {
                  void runAction(
                    `block-${device.id}`,
                    () => blockDevice(device.id),
                    `Device ${device.name} blocked`
                  )
                }
              }}
              onUnblockDevice={(peerId) => {
                void runAction(
                  `unblock-${peerId}`,
                  () => unblockDevice(peerId),
                  "Device unblocked"
                )
              }}
            />
          )}
          {page === "services" && (
            <ServicesPage
              state={state}
              busyKey={busyKey}
              onAdd={() => openDialog("service")}
              onRemove={(service) => {
                if (
                  window.confirm(`Remove “${service.name}” from this network?`)
                )
                  void runAction(
                    `remove-service-${service.id}`,
                    () =>
                      deleteResource(
                        `/api/services/${encodeURIComponent(service.id)}`
                      ),
                    `${service.name} removed`
                  )
              }}
              onStopForward={(id) => {
                if (window.confirm("Stop this TCP port forward?")) {
                  void runAction(
                    `stop-forward-${id}`,
                    () => deleteResource(`/api/forwards/${encodeURIComponent(id)}`),
                    "Port forward stopped"
                  )
                }
              }}
            />
          )}
          {page === "folders" && (
            <FoldersPage
              state={state}
              busyKey={busyKey}
              onCopyPath={(path) => void copyText(path)}
              copiedPath={copiedAddress}
              onAdd={() => openDialog("folder")}
              onReviewConflicts={(folder) => setConflictFolder(folder)}
              onSync={(folder) =>
                void runAction(
                  `sync-${folder.id}`,
                  () =>
                    postJson(
                      `/api/folders/${encodeURIComponent(folder.id)}/sync`,
                      {}
                    ),
                  `Sync request sent for ${folder.name}`
                )
              }
              onPause={(folder) =>
                void runAction(
                  `pause-${folder.id}`,
                  () =>
                    postJson(
                      `/api/folders/${encodeURIComponent(folder.id)}/pause`,
                      { paused: !folder.paused }
                    ),
                  folder.paused
                    ? `Sync resumed for ${folder.name}`
                    : `Sync paused for ${folder.name}`
                )
              }
              onRemove={(folder) => {
                if (
                  window.confirm(
                    `Stop tracking “${folder.name}”? Your files will stay on disk.`
                  )
                )
                  void runAction(
                    `remove-folder-${folder.id}`,
                    () =>
                      deleteResource(
                        `/api/folders/${encodeURIComponent(folder.id)}`
                      ),
                    `${folder.name} removed`
                  )
              }}
            />
          )}
          {page === "activity" && <ActivityPage activities={state.activity} />}
          {page === "settings" && (
            <SettingsPage
              state={state}
              busyKey={busyKey}
              onToggleNetwork={() =>
                void runAction(
                  "network",
                  () =>
                    postJson("/api/network", {
                      connected: !state.network.connected,
                    }),
                  state.network.connected
                    ? "Networking paused"
                    : "Networking resumed"
                )
              }
              onDiagnostics={() => void openDiagnostics()}
              onCopy={(address) => void copyText(address)}
              copiedAddress={copiedAddress}
              onAddRelay={async (address) =>
                await runAction(
                  "add-relay",
                  () => addRelay(address),
                  "Relay server added"
                )
              }
              onRemoveRelay={(address) => {
                if (window.confirm("Remove this relay server?")) {
                  void runAction(
                    `remove-relay-${address}`,
                    () => removeRelay(address),
                    "Relay server removed"
                  )
                }
              }}
            />
          )}
        </div>
      </main>

      <Dialog
        open={dialog === "device"}
        title="Pair or Connect Device"
        onClose={() => setDialog(null)}
        busy={busyKey === "dialog"}
        error={actionError}
        onDismissError={() => setActionError("")}
      >
        <DevicePairingModalContent
          onConnectAddress={(address) => {
            void runAction(
              "dialog",
              () => postJson("/api/peers", { address }),
              "Device connected"
            ).then((success) => {
              if (success) setDialog(null)
            })
          }}
          onClaimSuccess={(msg) => {
            setDialog(null)
            setNotice(msg)
            void refresh()
          }}
          busy={busyKey === "dialog"}
          onCancel={() => setDialog(null)}
        />
      </Dialog>

      <Dialog
        open={dialog === "service"}
        title="Publish a service"
        onClose={() => setDialog(null)}
        busy={busyKey === "dialog"}
        error={actionError}
        onDismissError={() => setActionError("")}
      >
        <form
          onSubmit={(event) => {
            const data = new FormData(event.currentTarget)
            const allowedStr = String(data.get("allowedPeers") || "").trim()
            const allowedPeers = allowedStr
              ? allowedStr
                  .split(",")
                  .map((s) => s.trim())
                  .filter(Boolean)
              : undefined
            void handleAction(
              event,
              () =>
                postJson("/api/services", {
                  name: String(data.get("name") || "").trim(),
                  target: String(data.get("target") || "").trim(),
                  trusted: data.get("trusted") === "on",
                  allowedPeers: allowedPeers,
                }),
              "Service published"
            )
          }}
        >
          <label className="field-label" htmlFor="service-name">
            Service name
          </label>
          <input
            className="field-input"
            id="service-name"
            name="name"
            required
            maxLength={64}
            placeholder="Project preview"
            autoFocus
          />
          <label
            className="field-label field-label-spaced"
            htmlFor="service-target"
          >
            Local address
          </label>
          <input
            className="field-input mono-input"
            id="service-target"
            name="target"
            type="url"
            required
            placeholder="http://127.0.0.1:3001"
          />
          <label
            className="field-label field-label-spaced"
            htmlFor="service-allow"
          >
            Access Control (Allowed Peer IDs)
          </label>
          <input
            className="field-input mono-input"
            id="service-allow"
            name="allowedPeers"
            placeholder="Optional comma-separated peer IDs (leave blank to allow all peers)"
          />
          <p className="field-help">
            Only devices listed here will be granted access. All other peers receive HTTP 403 Forbidden.
          </p>
          <div style={{ marginTop: "1rem", display: "flex", alignItems: "flex-start", gap: "0.5rem" }}>
            <input
              type="checkbox"
              id="service-trusted"
              name="trusted"
              style={{ marginTop: "0.25rem" }}
            />
            <div>
              <label htmlFor="service-trusted" style={{ fontWeight: 600, fontSize: "0.875rem", cursor: "pointer", display: "block" }}>
                Trusted App Mode
              </label>
              <p className="field-help" style={{ margin: "0.15rem 0 0 0" }}>
                Preserves Cookies, Authorization, and Origin headers, and relaxes CSP sandboxing for interactive development apps and WebSockets.
              </p>
            </div>
          </div>
          <p className="field-help" style={{ marginTop: "0.75rem" }}>
            Only services on this device’s loopback address can be published.
          </p>
          <FormActions
            onCancel={() => setDialog(null)}
            busy={busyKey === "dialog"}
            submitLabel="Publish service"
          />
        </form>
      </Dialog>

      <Dialog
        open={dialog === "folder"}
        title="Add a folder"
        onClose={() => setDialog(null)}
        busy={busyKey === "dialog"}
        error={actionError}
        onDismissError={() => setActionError("")}
      >
        <form
          onSubmit={(event) => {
            const data = new FormData(event.currentTarget)
            void handleAction(
              event,
              () =>
                postJson("/api/folders", {
                  name: String(data.get("name") || "").trim(),
                  path: String(data.get("path") || "").trim(),
                  peerId: String(data.get("peerId") || ""),
                  readOnly: data.get("readOnly") === "on",
                }),
              "Folder added"
            )
          }}
        >
          <label className="field-label" htmlFor="folder-name">
            Folder name
          </label>
          <input
            className="field-input"
            id="folder-name"
            name="name"
            required
            maxLength={64}
            placeholder="Design assets"
            autoFocus
          />
          <label
            className="field-label field-label-spaced"
            htmlFor="folder-path"
          >
            Full path on this device
          </label>
          <input
            className="field-input mono-input"
            id="folder-path"
            name="path"
            required
            placeholder="/Users/you/Projects/assets"
          />
          <p className="field-help">
            Enter an absolute path. Changes and deletions synchronize safely between
            devices with automatic conflict detection, delta chunking, and soft-delete archives.
          </p>
          <label
            className="field-label field-label-spaced"
            htmlFor="folder-peer"
          >
            Sync with
          </label>
          <select
            className="field-input"
            id="folder-peer"
            name="peerId"
            required
            defaultValue=""
          >
            <option value="" disabled>
              Select an online device
            </option>
            {state.devices
              .filter((device) => device.online && !device.self)
              .map((device) => (
                <option key={device.id} value={device.id}>
                  {device.name}
                </option>
              ))}
          </select>
          <div style={{ marginTop: "1rem", display: "flex", alignItems: "flex-start", gap: "0.5rem" }}>
            <input
              type="checkbox"
              id="folder-readonly"
              name="readOnly"
              style={{ marginTop: "0.25rem" }}
            />
            <div>
              <label htmlFor="folder-readonly" style={{ fontWeight: 600, fontSize: "0.875rem", cursor: "pointer", display: "block" }}>
                Read-Only Sync
              </label>
              <p className="field-help" style={{ margin: "0.15rem 0 0 0" }}>
                Send files out without allowing remote peers to overwrite or delete local files.
              </p>
            </div>
          </div>
          {state.devices.filter((device) => device.online && !device.self)
            .length === 0 && (
            <p className="field-help field-error">
              Connect an online device before adding a synced folder.
            </p>
          )}
          <FormActions
            onCancel={() => setDialog(null)}
            busy={busyKey === "dialog"}
            submitLabel="Add folder"
            disabled={
              state.devices.filter((device) => device.online && !device.self)
                .length === 0
            }
          />
        </form>
      </Dialog>

      <Dialog
        open={dialog === "diagnostics"}
        title="Network diagnostics"
        onClose={() => setDialog(null)}
        busy={diagnosticsLoading}
        error={actionError}
        onDismissError={() => setActionError("")}
      >
        {diagnosticsLoading ? (
          <div className="dialog-loading">
            <LoaderCircle size={18} className="spin" /> Loading diagnostics…
          </div>
        ) : diagnostics ? (
          <pre className="diagnostics-output">
            {JSON.stringify(diagnostics, null, 2)}
          </pre>
        ) : (
          <EmptyBlock
            icon={<CircleHelp size={19} />}
            title="Diagnostics unavailable"
          />
        )}
        <div className="dialog-actions">
          <button
            type="button"
            className="button button-secondary"
            onClick={() => setDialog(null)}
          >
            Close
          </button>
        </div>
      </Dialog>

      <ConflictsDialog
        folder={conflictFolder}
        onClose={() => setConflictFolder(null)}
        onResolved={() => void refresh()}
      />
    </div>
  )
}

function Overview({
  state,
  busyKey,
  onDevice,
  onService,
  onFolder,
  onDemo,
  onPage,
  onSync,
  onReviewConflicts,
}: {
  state: AppState
  busyKey: string
  onDevice: () => void
  onService: () => void
  onFolder: () => void
  onDemo: () => void
  onPage: (page: PageKey) => void
  onSync: (folder: SharedFolder) => void
  onReviewConflicts?: (folder: SharedFolder) => void
}) {
  const onlinePeers = state.devices.filter(
    (device) => device.online && !device.self
  )
  const recentServices = state.services.slice(0, 3)
  const recentFolders = state.folders.slice(0, 3)
  return (
    <>
      <div className="stats-strip" aria-label="Network summary">
        <Stat
          icon={<Radio size={17} />}
          label="Online devices"
          value={state.stats.onlineDevices}
          suffix={`of ${state.devices.length}`}
        />
        <Stat
          icon={<Globe2 size={17} />}
          label="Services"
          value={state.stats.services}
        />
        <Stat
          icon={<FolderSync size={17} />}
          label="Synced files"
          value={state.stats.syncedFiles}
        />
        <Stat
          icon={<ArrowDownToLine size={17} />}
          label="Transferred"
          value={formatBytes(state.stats.transferredBytes)}
        />
      </div>

      <div className="overview-primary-grid">
        <section className="panel topology-panel">
          <PanelHeader
            title="Network topology"
            icon={<Network size={17} />}
            action={
              <span
                className={`live-label ${state.network.connected ? "" : "is-paused"}`}
              >
                <i />
                {state.network.connected ? "Live" : "Paused"}
              </span>
            }
          />
          {state.devices.length === 0 ? (
            <EmptyBlock
              icon={<Network size={20} />}
              title="No devices yet"
              action={
                <button
                  className="text-button"
                  onClick={() => onPage("devices")}
                >
                  View devices <ArrowUpRight size={14} />
                </button>
              }
            />
          ) : (
            <Topology state={state} />
          )}
          <div className="topology-legend">
            <span>
              <i className="legend-direct" /> Direct
            </span>
            <span>
              <i className="legend-relay" /> Relayed
            </span>
            <span>
              <i className="legend-local" /> This device
            </span>
          </div>
        </section>

        <section className="panel connections-panel">
          <PanelHeader
            title="Devices"
            detail={`${onlinePeers.length} online`}
            icon={<Laptop size={17} />}
            action={
              <button className="quiet-link" onClick={() => onPage("devices")}>
                All devices <ArrowUpRight size={13} />
              </button>
            }
          />
          {state.devices.length === 0 ? (
            <EmptyBlock
              icon={<Laptop size={18} />}
              title="No devices connected"
            />
          ) : (
            <div className="device-mini-list">
              {state.devices.slice(0, 4).map((device) => (
                <DeviceRow key={device.id} device={device} compact />
              ))}
            </div>
          )}
          {state.network.demo && (
            <button
              className="demo-change"
              onClick={onDemo}
              disabled={busyKey === "demo"}
            >
              <span className="demo-change-icon">
                <Zap size={15} />
              </span>
              <span>
                <strong>Change sample file</strong>
                {busyKey === "demo" && <small>Writing and syncing…</small>}
              </span>
              <ArrowUpRight size={14} />
            </button>
          )}
        </section>
      </div>

      <div className="overview-secondary-grid">
        <section className="panel list-panel">
          <PanelHeader
            title="Services"
            detail={
              state.services.length
                ? `${state.services.length} published`
                : undefined
            }
            icon={<Globe2 size={17} />}
            action={
              <button className="quiet-link" onClick={() => onPage("services")}>
                View all <ArrowUpRight size={13} />
              </button>
            }
          />
          {recentServices.length > 0 ? (
            <ServiceList services={recentServices} compact />
          ) : (
            <EmptyBlock
              icon={<Globe2 size={18} />}
              title="No services published"
              action={
                <button className="text-button" onClick={onService}>
                  <Plus size={14} /> Publish a service
                </button>
              }
            />
          )}
        </section>
        <section className="panel list-panel">
          <PanelHeader
            title="Folders"
            detail={
              state.folders.length
                ? `${state.folders.length} tracked`
                : undefined
            }
            icon={<Folder size={17} />}
            action={
              <button className="quiet-link" onClick={() => onPage("folders")}>
                View all <ArrowUpRight size={13} />
              </button>
            }
          />
          {recentFolders.length > 0 ? (
            <FolderList
              folders={recentFolders}
              busyKey={busyKey}
              onSync={onSync}
              onReviewConflicts={onReviewConflicts}
              compact
            />
          ) : (
            <EmptyBlock
              icon={<Folder size={18} />}
              title="No folders syncing"
              action={
                <button className="text-button" onClick={onFolder}>
                  <Plus size={14} /> Add a folder
                </button>
              }
            />
          )}
        </section>
      </div>

      {state.activity.length > 0 && (
        <section className="panel activity-preview">
          <PanelHeader
            title="Recent activity"
            icon={<ActivityIcon size={17} />}
            action={
              <button className="quiet-link" onClick={() => onPage("activity")}>
                View activity <ArrowUpRight size={13} />
              </button>
            }
          />
          <ActivityList activities={state.activity.slice(0, 4)} compact />
        </section>
      )}
    </>
  )
}

function DevicePairingModalContent({
  onConnectAddress,
  onClaimSuccess,
  busy,
  onCancel,
}: {
  onConnectAddress: (address: string) => void
  onClaimSuccess: (msg: string) => void
  busy: boolean
  onCancel: () => void
}) {
  const [tab, setTab] = useState<"pair" | "join" | "multiaddress">("pair")
  const [session, setSession] = useState<PairingSession | null>(null)
  const [generating, setGenerating] = useState(false)
  const [error, setError] = useState("")
  const [timeLeft, setTimeLeft] = useState<number>(0)
  const [pinInput, setPinInput] = useState("")
  const [relayInput, setRelayInput] = useState("")
  const [joining, setJoining] = useState(false)

  // Countdown timer for pairing session
  useEffect(() => {
    if (!session?.expiresAt) return
    const update = () => {
      const exp = new Date(session.expiresAt).getTime()
      const diff = Math.max(0, Math.floor((exp - Date.now()) / 1000))
      setTimeLeft(diff)
      if (diff === 0) setSession(null)
    }
    update()
    const interval = setInterval(update, 1000)
    return () => clearInterval(interval)
  }, [session])

  const handleGenerate = async () => {
    setGenerating(true)
    setError("")
    try {
      const s = await generatePairingCode(600)
      setSession(s)
    } catch (err) {
      setError(
        err instanceof Error ? err.message : "Failed to generate pairing PIN"
      )
    } finally {
      setGenerating(false)
    }
  }

  const handleJoin = async (e: FormEvent) => {
    e.preventDefault()
    if (!pinInput.trim()) return
    setJoining(true)
    setError("")
    try {
      const payload = await claimPairingCode(
        pinInput.trim(),
        relayInput.trim() || undefined
      )
      onClaimSuccess(`Paired successfully with network ${payload.networkId}!`)
    } catch (err) {
      setError(
        err instanceof Error
          ? err.message
          : "Pairing failed. Check the PIN and relay address."
      )
    } finally {
      setJoining(false)
    }
  }

  const formatTimer = (secs: number) => {
    const m = Math.floor(secs / 60)
    const s = secs % 60
    return `${m}:${s < 10 ? "0" : ""}${s}`
  }

  return (
    <div>
      <div
        style={{
          display: "flex",
          gap: "0.5rem",
          marginBottom: "1.25rem",
          borderBottom: "1px solid var(--border, #333)",
          paddingBottom: "0.75rem",
        }}
      >
        <button
          type="button"
          className={`button ${tab === "pair" ? "button-primary" : "button-secondary"} button-sm`}
          onClick={() => {
            setTab("pair")
            setError("")
          }}
        >
          <Zap size={14} /> Pair this Device
        </button>
        <button
          type="button"
          className={`button ${tab === "join" ? "button-primary" : "button-secondary"} button-sm`}
          onClick={() => {
            setTab("join")
            setError("")
          }}
        >
          <Key size={14} /> Enter PIN to Join
        </button>
        <button
          type="button"
          className={`button ${tab === "multiaddress" ? "button-primary" : "button-secondary"} button-sm`}
          onClick={() => {
            setTab("multiaddress")
            setError("")
          }}
        >
          <Link2 size={14} /> Multiaddress
        </button>
      </div>

      {error && (
        <div
          className="connection-warning"
          role="alert"
          style={{ marginBottom: "1rem" }}
        >
          <AlertTriangle size={16} /> {error}
        </div>
      )}

      {tab === "pair" && (
        <div>
          <p className="field-help" style={{ marginBottom: "1rem" }}>
            Generate a short-lived, encrypted 6-digit PIN. Another device can
            pair instantly without manually copying 64-character private network
            keys.
          </p>

          {!session ? (
            <button
              type="button"
              className="button button-primary"
              onClick={handleGenerate}
              disabled={generating}
              style={{
                width: "100%",
                justifyContent: "center",
                padding: "0.75rem",
              }}
            >
              {generating ? (
                <LoaderCircle size={16} className="spin" />
              ) : (
                <Zap size={16} />
              )}
              Generate 6-Digit Pairing PIN
            </button>
          ) : (
            <div
              style={{
                background: "var(--card-bg, #1e1e24)",
                border: "1px solid var(--border-focus, #3b82f6)",
                borderRadius: "8px",
                padding: "1.25rem",
                textAlign: "center",
              }}
            >
              <div
                style={{
                  fontSize: "0.75rem",
                  textTransform: "uppercase",
                  letterSpacing: "0.1em",
                  color: "#9ca3af",
                  marginBottom: "0.5rem",
                }}
              >
                Pairing PIN (Valid for 10 Minutes)
              </div>
              <div
                style={{
                  fontSize: "2.25rem",
                  fontWeight: 700,
                  letterSpacing: "0.2em",
                  fontFamily: "monospace",
                  color: "#38bdf8",
                  margin: "0.5rem 0",
                }}
              >
                {session.formatted}
              </div>
              <div
                style={{
                  fontSize: "0.85rem",
                  color: timeLeft < 60 ? "#ef4444" : "#eab308",
                  marginBottom: "1rem",
                }}
              >
                Expires in {formatTimer(timeLeft)}
              </div>
              <p
                className="field-help"
                style={{ textAlign: "left", marginBottom: "0.5rem" }}
              >
                On the joining device, select{" "}
                <strong>Enter PIN to Join</strong>, or run in terminal:
              </p>
              <div
                style={{
                  background: "#0d1117",
                  padding: "0.5rem 0.75rem",
                  borderRadius: "6px",
                  fontFamily: "monospace",
                  fontSize: "0.85rem",
                  textAlign: "left",
                  color: "#e6edf3",
                  marginBottom: "1rem",
                  overflowX: "auto",
                }}
              >
                loom pair join {session.formatted}
              </div>
              <button
                type="button"
                className="button button-secondary button-sm"
                onClick={handleGenerate}
                disabled={generating}
              >
                <RefreshCw size={14} /> Generate new PIN
              </button>
            </div>
          )}
          <div
            style={{
              marginTop: "1.5rem",
              display: "flex",
              justifyContent: "flex-end",
            }}
          >
            <button
              type="button"
              className="button button-secondary"
              onClick={onCancel}
            >
              Done
            </button>
          </div>
        </div>
      )}

      {tab === "join" && (
        <form onSubmit={handleJoin}>
          <label className="field-label" htmlFor="pairing-pin-input">
            6-Digit Pairing PIN
          </label>
          <input
            className="field-input mono-input"
            id="pairing-pin-input"
            value={pinInput}
            onChange={(e) => setPinInput(e.target.value)}
            required
            maxLength={7}
            placeholder="123-456"
            autoFocus
            style={{
              fontSize: "1.25rem",
              letterSpacing: "0.15em",
              textAlign: "center",
            }}
          />
          <p className="field-help">
            Enter the 6-digit PIN displayed on the authorizer device.
          </p>

          <label
            className="field-label field-label-spaced"
            htmlFor="pairing-relay-input"
          >
            Relay Multiaddress (Optional)
          </label>
          <input
            className="field-input mono-input"
            id="pairing-relay-input"
            value={relayInput}
            onChange={(e) => setRelayInput(e.target.value)}
            placeholder="/ip4/203.0.113.10/tcp/4001/p2p/..."
          />
          <p className="field-help">
            Leave blank if both devices share a configured relay or local network.
          </p>

          <FormActions
            onCancel={onCancel}
            busy={joining}
            submitLabel="Pair & Connect"
          />
        </form>
      )}

      {tab === "multiaddress" && (
        <form
          onSubmit={(event) => {
            event.preventDefault()
            const data = new FormData(event.currentTarget)
            const addr = String(data.get("address") || "").trim()
            if (addr) onConnectAddress(addr)
          }}
        >
          <label className="field-label" htmlFor="device-address">
            Direct Multiaddress
          </label>
          <input
            className="field-input mono-input"
            id="device-address"
            name="address"
            required
            placeholder="/ip4/192.168.1.20/tcp/4001/p2p/…"
            autoFocus
          />
          <p className="field-help">
            Paste the device’s full libp2p multiaddress. Both devices must share
            the same network key.
          </p>
          <FormActions
            onCancel={onCancel}
            busy={busy}
            submitLabel="Connect device"
          />
        </form>
      )}
    </div>
  )
}

function DevicesPage({
  state,
  onAdd,
  onCopy,
  copied,
  onBlockDevice,
  onUnblockDevice,
}: {
  state: AppState
  onAdd: () => void
  onCopy: () => void
  copied: boolean
  onBlockDevice?: (device: Device) => void
  onUnblockDevice?: (peerId: string) => void
}) {
  const onlineCount = state.devices.filter((device) => device.online).length
  return (
    <>
      <section className="panel topology-panel topology-large">
        <PanelHeader
          title="Network topology"
          detail={`${onlineCount} of ${state.devices.length} devices online`}
          icon={<Network size={17} />}
          action={
            <span
              className={`live-label ${state.network.connected ? "" : "is-paused"}`}
            >
              <i />
              {state.network.connected ? "Live" : "Paused"}
            </span>
          }
        />
        {state.devices.length ? (
          <Topology state={state} large />
        ) : (
          <EmptyBlock
            icon={<Network size={20} />}
            title="No other devices yet"
            action={
              <button className="button button-primary" onClick={onAdd}>
                <Plus size={15} /> Add device
              </button>
            }
          />
        )}
        <div className="topology-legend">
          <span>
            <i className="legend-direct" /> Direct connection
          </span>
          <span>
            <i className="legend-relay" /> Circuit relay
          </span>
          <span>
            <i className="legend-local" /> This device
          </span>
        </div>
      </section>
      <section className="panel data-panel">
        <PanelHeader
          title="All devices"
          detail={
            state.devices.length ? `${state.devices.length} devices` : undefined
          }
          icon={<Laptop size={17} />}
          action={
            state.network.addresses.length > 0 ? (
              <button className="quiet-link" onClick={onCopy}>
                {copied ? <Check size={14} /> : <Copy size={14} />}
                {copied ? "Copied address" : "Copy this device’s address"}
              </button>
            ) : null
          }
        />
        {state.devices.length ? (
          <div className="table-scroll">
            <table>
              <thead>
                <tr>
                  <th>Device</th>
                  <th>Status</th>
                  <th>Connection</th>
                  <th>Address</th>
                  <th>Last seen</th>
                  <th className="th-actions">Actions</th>
                </tr>
              </thead>
              <tbody>
                {state.devices.map((device) => (
                  <DeviceTableRow
                    key={device.id}
                    device={device}
                    onBlock={onBlockDevice}
                  />
                ))}
              </tbody>
            </table>
          </div>
        ) : (
          <EmptyBlock
            icon={<Laptop size={19} />}
            title="No devices in this network"
            action={
              <button className="text-button" onClick={onAdd}>
                <Plus size={14} /> Add a device
              </button>
            }
          />
        )}

        {state.blockedDevices && state.blockedDevices.length > 0 && (
          <div style={{ marginTop: "1.5rem", borderTop: "1px solid var(--border, #333)", paddingTop: "1rem" }}>
            <PanelHeader
              title={`Blocked devices (${state.blockedDevices.length})`}
              detail="Connections from these peer IDs are rejected at transport handshake"
              icon={<Ban size={17} />}
            />
            <div className="table-scroll">
              <table>
                <thead>
                  <tr>
                    <th>Peer ID</th>
                    <th className="th-actions">Action</th>
                  </tr>
                </thead>
                <tbody>
                  {state.blockedDevices.map((peerId) => (
                    <tr key={peerId}>
                      <td>
                        <span className="mono-cell">{peerId}</span>
                      </td>
                      <td className="td-actions">
                        <div className="row-actions">
                          <button
                            type="button"
                            className="button button-secondary button-sm"
                            onClick={() => onUnblockDevice?.(peerId)}
                          >
                            <Unlock size={14} /> Unblock
                          </button>
                        </div>
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          </div>
        )}
      </section>
    </>
  )
}

function ServicesPage({
  state,
  busyKey,
  onAdd,
  onRemove,
  onStopForward,
}: {
  state: AppState
  busyKey: string
  onAdd: () => void
  onRemove: (service: Service) => void
  onStopForward?: (id: string) => void
}) {
  return (
    <>
      <section className="panel data-panel">
        <PanelHeader title="Published services" icon={<Globe2 size={17} />} />
        {state.services.length ? (
          <div className="table-scroll">
            <table>
              <thead>
                <tr>
                  <th>Service</th>
                  <th>Device</th>
                  <th>Status</th>
                  <th>Target</th>
                  <th className="th-actions">Actions</th>
                </tr>
              </thead>
              <tbody>
                {state.services.map((service) => {
                  const locallyOwned =
                    Boolean(state.network.peerId) &&
                    service.deviceId === state.network.peerId
                  return (
                    <tr key={service.id}>
                      <td>
                        <div className="table-primary">
                          <span className="row-icon service-row-icon">
                            <Globe2 size={16} />
                          </span>
                          <span>
                            <strong>{service.name}</strong>
                            <small>
                              {service.protocol.toUpperCase()} · port{" "}
                              {service.port}
                              {service.trusted && (
                                <span
                                  style={{
                                    marginLeft: "0.5rem",
                                    padding: "0.1rem 0.4rem",
                                    borderRadius: "4px",
                                    fontSize: "0.7rem",
                                    fontWeight: 600,
                                    background: "rgba(16, 185, 129, 0.15)",
                                    color: "#10b981",
                                    border: "1px solid rgba(16, 185, 129, 0.3)",
                                  }}
                                  title="Trusted app (Origin, Cookies, and Auth headers preserved)"
                                >
                                  Trusted
                                </span>
                              )}
                            </small>
                          </span>
                        </div>
                      </td>
                      <td>
                        {service.deviceName ||
                          (locallyOwned ? "This device" : "Unknown device")}
                      </td>
                      <td>
                        <StatusLabel status={service.status} />
                      </td>
                      <td>
                        <span className="mono-cell">{service.target}</span>
                      </td>
                      <td>
                        <div className="row-actions">
                          <a
                            className="icon-action"
                            href={service.url}
                            target="_blank"
                            rel="noreferrer"
                            aria-label={`Open ${service.name}`}
                            title="Open service"
                          >
                            <ArrowUpRight size={16} />
                          </a>
                          {locallyOwned && (
                            <button
                              className="icon-action danger-action"
                              onClick={() => onRemove(service)}
                              disabled={
                                busyKey === `remove-service-${service.id}`
                              }
                              aria-label={`Remove ${service.name}`}
                              title="Remove service"
                            >
                              {busyKey === `remove-service-${service.id}` ? (
                                <LoaderCircle size={16} className="spin" />
                              ) : (
                                <Trash2 size={16} />
                              )}
                            </button>
                          )}
                        </div>
                      </td>
                    </tr>
                  )
                })}
              </tbody>
            </table>
          </div>
        ) : (
          <EmptyBlock
            icon={<Globe2 size={20} />}
            title="No services published"
            action={
              <button className="button button-primary" onClick={onAdd}>
                <Plus size={15} /> Publish service
              </button>
            }
          />
        )}
      </section>

      <section className="panel data-panel" style={{ marginTop: "1.5rem" }}>
        <PanelHeader title="Active TCP port forwards" icon={<Network size={17} />} />
        {state.forwards && state.forwards.length ? (
          <div className="table-scroll">
            <table>
              <thead>
                <tr>
                  <th>Local listener</th>
                  <th>Destination peer</th>
                  <th>Remote target</th>
                  <th>Status</th>
                  <th className="th-actions">Actions</th>
                </tr>
              </thead>
              <tbody>
                {state.forwards.map((forward) => (
                  <tr key={forward.id}>
                    <td>
                      <div className="table-primary">
                        <span className="row-icon service-row-icon">
                          <Network size={16} />
                        </span>
                        <span>
                          <strong className="mono-cell">127.0.0.1:{forward.localPort}</strong>
                          <small>{forward.id}</small>
                        </span>
                      </div>
                    </td>
                    <td>{forward.peerName || forward.peerId}</td>
                    <td>
                      <span className="mono-cell">{forward.target}</span>
                    </td>
                    <td>
                      <StatusLabel status={forward.status} />
                    </td>
                    <td>
                      <div className="row-actions">
                        <button
                          className="icon-action danger-action"
                          onClick={() => onStopForward?.(forward.id)}
                          disabled={busyKey === `stop-forward-${forward.id}`}
                          aria-label="Stop port forward"
                          title="Stop port forward"
                        >
                          {busyKey === `stop-forward-${forward.id}` ? (
                            <LoaderCircle size={16} className="spin" />
                          ) : (
                            <Trash2 size={16} />
                          )}
                        </button>
                      </div>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        ) : (
          <EmptyBlock
            icon={<Network size={20} />}
            title="No active TCP port forwards"
            detail="Forward local TCP ports directly to remote peers and services using: loom forward <peer>:<port>"
          />
        )}
      </section>
    </>
  )
}

function FoldersPage({
  state,
  busyKey,
  onAdd,
  onSync,
  onPause,
  onRemove,
  onCopyPath,
  copiedPath,
  onReviewConflicts,
}: {
  state: AppState
  busyKey: string
  onAdd: () => void
  onSync: (folder: SharedFolder) => void
  onPause: (folder: SharedFolder) => void
  onRemove: (folder: SharedFolder) => void
  onCopyPath: (path: string) => void
  copiedPath: string
  onReviewConflicts: (folder: SharedFolder) => void
}) {
  return (
    <div className="folder-panels">
      <section className="panel data-panel">
        <PanelHeader title="Sending folders" icon={<FolderSync size={17} />} />
        {state.folders.length ? (
          <div className="table-scroll">
            <table>
              <thead>
                <tr>
                  <th>Folder</th>
                  <th>Device</th>
                  <th>Status</th>
                  <th>Contents</th>
                  <th>Last sync</th>
                  <th className="th-actions">Actions</th>
                </tr>
              </thead>
              <tbody>
                {state.folders.map((folder) => (
                  <tr key={folder.id}>
                    <td>
                      <div className="table-primary">
                        <span className="row-icon folder-row-icon">
                          <Folder size={16} />
                        </span>
                        <span>
                          <strong>{folder.name}</strong>
                          <small className="mono-cell" title={folder.path}>
                            {folder.path}
                          </small>
                          <button
                            className="text-button"
                            onClick={() => onCopyPath(folder.path)}
                          >
                            {copiedPath === folder.path
                              ? "Copied path"
                              : "Copy path"}
                          </button>
                        </span>
                      </div>
                    </td>
                    <td>{folder.peerName || "Unknown device"}</td>
                    <td>
                      <StatusLabel status={folderStatus(folder)} />
                      {Boolean(folder.conflicts && folder.conflicts > 0) && (
                        <div>
                          <button
                            type="button"
                            className="conflict-badge-btn"
                            onClick={() => onReviewConflicts(folder)}
                            title="Review and resolve conflicts"
                          >
                            <AlertTriangle size={12} />
                            <span>
                              {folder.conflicts}{" "}
                              {folder.conflicts === 1 ? "conflict" : "conflicts"}
                            </span>
                          </button>
                        </div>
                      )}
                      {folder.error && (
                        <small className="folder-error">{folder.error}</small>
                      )}
                      <FolderProgress folder={folder} />
                    </td>
                    <td>
                      {folder.files} files
                      <span className="muted-cell">
                        {" "}
                        · {formatBytes(folder.bytes)}
                      </span>
                    </td>
                    <td>{formatRelativeTime(folder.lastSync)}</td>
                    <td>
                      <div className="row-actions">
                        <button
                          className="icon-action"
                          onClick={() => onSync(folder)}
                          disabled={busyKey === `sync-${folder.id}`}
                          aria-label={`Sync ${folder.name}`}
                          title="Sync now"
                        >
                          {busyKey === `sync-${folder.id}` ? (
                            <LoaderCircle size={16} className="spin" />
                          ) : (
                            <RefreshCw size={16} />
                          )}
                        </button>
                        <button
                          className="icon-action"
                          onClick={() => onPause(folder)}
                          disabled={busyKey === `pause-${folder.id}`}
                          aria-label={
                            folder.paused
                              ? `Resume ${folder.name}`
                              : `Pause ${folder.name}`
                          }
                          title={folder.paused ? "Resume sync" : "Pause sync"}
                        >
                          {folder.paused ? (
                            <Radio size={16} />
                          ) : (
                            <WifiOff size={16} />
                          )}
                        </button>
                        <button
                          className="icon-action danger-action"
                          onClick={() => onRemove(folder)}
                          disabled={busyKey === `remove-folder-${folder.id}`}
                          aria-label={`Remove ${folder.name}`}
                          title="Stop tracking"
                        >
                          {busyKey === `remove-folder-${folder.id}` ? (
                            <LoaderCircle size={16} className="spin" />
                          ) : (
                            <Trash2 size={16} />
                          )}
                        </button>
                      </div>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        ) : (
          <EmptyBlock
            icon={<Folder size={20} />}
            title="No folders syncing"
            action={
              <button className="button button-primary" onClick={onAdd}>
                <Plus size={15} /> Add folder
              </button>
            }
          />
        )}
      </section>
      <section className="panel data-panel">
        <PanelHeader title="Received folders" icon={<Folder size={17} />} />
        {state.receivedFolders?.length ? (
          <div className="table-scroll">
            <table>
              <thead>
                <tr>
                  <th>Folder and local path</th>
                  <th>From device</th>
                  <th>Files</th>
                  <th>Last received</th>
                </tr>
              </thead>
              <tbody>
                {state.receivedFolders.map((folder) => (
                  <tr key={folder.id}>
                    <td>
                      <div className="table-primary">
                        <span className="row-icon folder-row-icon">
                          <Folder size={16} />
                        </span>
                        <span>
                          <strong>{folder.name}</strong>
                          <small className="mono-cell" title={folder.path}>
                            {folder.path}
                          </small>
                          <button
                            className="text-button"
                            onClick={() => onCopyPath(folder.path)}
                          >
                            {copiedPath === folder.path
                              ? "Copied path"
                              : "Copy path"}
                          </button>
                        </span>
                      </div>
                    </td>
                    <td>{folder.peerName || folder.peerId}</td>
                    <td>
                      <details>
                        <summary>
                          {folder.files.length}{" "}
                          {folder.files.length === 1 ? "file" : "files"} ·{" "}
                          {formatBytes(folder.bytes)}
                        </summary>
                        <ul>
                          {folder.files.map((file) => (
                            <li key={file.path}>
                              <a
                                className="text-button"
                                href={`/api/received/${encodeURIComponent(folder.id)}/files/${file.path.split("/").map(encodeURIComponent).join("/")}`}
                                download
                              >
                                {file.path}
                              </a>
                              <span className="muted-cell">
                                {" "}
                                · {formatBytes(file.size)}
                              </span>
                            </li>
                          ))}
                        </ul>
                      </details>
                    </td>
                    <td>{formatRelativeTime(folder.lastReceived)}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        ) : (
          <EmptyBlock
            icon={<Folder size={20} />}
            title="No files received yet"
            detail="On the other computer, add a source folder and select this device. Received files and their local path will appear here automatically."
          />
        )}
      </section>
    </div>
  )
}

function ActivityPage({ activities }: { activities: Activity[] }) {
  return (
    <section className="panel data-panel activity-panel">
      <PanelHeader title="Network activity" icon={<ActivityIcon size={17} />} />
      {activities.length ? (
        <ActivityList activities={activities} />
      ) : (
        <EmptyBlock icon={<FileClock size={20} />} title="No activity yet" />
      )}
    </section>
  )
}

function SettingsPage({
  state,
  busyKey,
  onToggleNetwork,
  onDiagnostics,
  onCopy,
  copiedAddress,
  onAddRelay,
  onRemoveRelay,
}: {
  state: AppState
  busyKey: string
  onToggleNetwork: () => void
  onDiagnostics: () => void
  onCopy: (address: string) => void
  copiedAddress: string
  onAddRelay: (address: string) => Promise<boolean>
  onRemoveRelay: (address: string) => void
}) {
  const self = state.devices.find((device) => device.self)
  const [relays, setRelays] = useState<RelayInfo[]>([])
  const [newRelayAddr, setNewRelayAddr] = useState("")

  const loadRelays = useCallback(async () => {
    try {
      const data = await getRelays()
      setRelays(data || [])
    } catch {
      // ignore
    }
  }, [])

  useEffect(() => {
    void loadRelays()
    const timer = setInterval(() => void loadRelays(), 4000)
    return () => clearInterval(timer)
  }, [loadRelays, state])

  return (
    <div className="settings-grid">
      <section className="panel settings-panel">
        <PanelHeader title="Network" icon={<Network size={17} />} />
        <div className="setting-row">
          <span className="setting-icon">
            <Wifi size={17} />
          </span>
          <span className="setting-copy">
            <strong>Networking</strong>
            <small>
              {state.network.connected
                ? "This device can discover peers and sync folders."
                : "Peer connections and folder sync are paused."}
            </small>
          </span>
          <button
            className={`toggle-switch ${state.network.connected ? "on" : ""}`}
            role="switch"
            aria-checked={state.network.connected}
            aria-label="Enable networking"
            onClick={onToggleNetwork}
            disabled={busyKey === "network"}
          >
            <span />
          </button>
        </div>
        <div className="setting-row">
          <span className="setting-icon">
            <Network size={17} />
          </span>
          <span className="setting-copy">
            <strong>Network identity</strong>
            <small className="mono-cell">
              {state.network.id || state.network.peerId || "Unavailable"}
            </small>
          </span>
        </div>
        {self && (
          <div className="setting-row">
            <span className="setting-icon">
              <Laptop size={17} />
            </span>
            <span className="setting-copy">
              <strong>This device</strong>
              <small>
                {self.name} · {self.platform || "Unknown platform"}
              </small>
            </span>
            <span className="setting-value">v{state.network.version}</span>
          </div>
        )}
      </section>
      <section className="panel settings-panel">
        <PanelHeader title="Connection details" icon={<Link2 size={17} />} />
        {state.network.addresses.length ? (
          <div className="address-list">
            {state.network.addresses.map((address) => (
              <div className="address-item" key={address}>
                <code>{address}</code>
                <button
                  className="icon-action"
                  onClick={() => onCopy(address)}
                  title="Copy address"
                  aria-label="Copy device address"
                >
                  {copiedAddress === address ? (
                    <Check size={15} />
                  ) : (
                    <Copy size={15} />
                  )}
                </button>
              </div>
            ))}
          </div>
        ) : (
          <EmptyBlock
            icon={<Link2 size={18} />}
            title="No addresses available"
            detail="No connection address has been reported."
          />
        )}
      </section>
      <section className="panel settings-panel">
        <PanelHeader title="Circuit Relays & Rendezvous" icon={<Radio size={17} />} />
        <div style={{ padding: "0 20px 12px", color: "var(--color-muted, #86868b)", fontSize: "13px", lineHeight: "1.5" }}>
          Relays provide zero-knowledge rendezvous discovery and enable direct hole punching (DCUtR) or fallback circuit tunneling across symmetric NATs.
        </div>
        {relays.length ? (
          <div className="address-list">
            {relays.map((relay) => (
              <div className="address-item" key={relay.address} style={{ display: "flex", alignItems: "center", justifyContent: "space-between", gap: "8px" }}>
                <div style={{ display: "flex", alignItems: "center", gap: "8px", overflow: "hidden", textOverflow: "ellipsis" }}>
                  <span
                    style={{
                      width: "8px",
                      height: "8px",
                      borderRadius: "50%",
                      backgroundColor: relay.status === "connected" ? "#34c759" : "#ff9500",
                      display: "inline-block",
                      flexShrink: 0,
                    }}
                    title={relay.status}
                  />
                  <code style={{ fontSize: "12px", wordBreak: "break-all" }}>{relay.address}</code>
                </div>
                <div style={{ display: "flex", gap: "4px", flexShrink: 0 }}>
                  <button
                    className="icon-action"
                    onClick={() => onCopy(relay.address)}
                    title="Copy address"
                    aria-label="Copy relay address"
                  >
                    {copiedAddress === relay.address ? <Check size={15} /> : <Copy size={15} />}
                  </button>
                  <button
                    className="icon-action"
                    onClick={() => onRemoveRelay(relay.address)}
                    title="Remove relay"
                    aria-label="Remove relay"
                  >
                    <Trash2 size={15} />
                  </button>
                </div>
              </div>
            ))}
          </div>
        ) : (
          <EmptyBlock
            icon={<Radio size={18} />}
            title="No relays configured"
            detail="Devices behind separate symmetric firewalls cannot discover each other without a relay."
          />
        )}
        <form
          onSubmit={async (e) => {
            e.preventDefault()
            const trimmed = newRelayAddr.trim()
            if (!trimmed) return
            const ok = await onAddRelay(trimmed)
            if (ok) {
              setNewRelayAddr("")
              void loadRelays()
            }
          }}
          style={{ display: "flex", gap: "8px", padding: "16px 20px" }}
        >
          <input
            type="text"
            className="input"
            placeholder="/ip4/<ip>/tcp/<port>/p2p/<peer-id>"
            value={newRelayAddr}
            onChange={(e) => setNewRelayAddr(e.target.value)}
            style={{ flex: 1, fontSize: "13px", padding: "8px 12px" }}
          />
          <button
            type="submit"
            className="button button-primary"
            disabled={!newRelayAddr.trim() || busyKey === "add-relay"}
            style={{ display: "flex", alignItems: "center", gap: "4px" }}
          >
            <Plus size={15} /> Add Relay
          </button>
        </form>
      </section>
      <section className="panel settings-panel diagnostics-card">
        <PanelHeader title="Diagnostics" icon={<Settings2 size={17} />} />
        <button className="button button-secondary" onClick={onDiagnostics}>
          <Clipboard size={15} /> View diagnostics
        </button>
      </section>
    </div>
  )
}

function Topology({
  state,
  large = false,
}: {
  state: AppState
  large?: boolean
}) {
  const self = state.devices.find((device) => device.self)
  if (!self) {
    return (
      <EmptyBlock
        icon={<Network size={20} />}
        title="This device is not reported"
        detail="The API has not reported a local device."
      />
    )
  }
  const peers = state.devices.filter((device) => !device.self)
  const online = peers.filter((device) => device.online)
  const offline = peers.filter((device) => !device.online)
  const width = large ? 900 : 700
  const baseHeight = large ? 330 : 280
  const peerSlots = Math.min(Math.max(online.length, 1), 5)
  const spread = peerSlots > 2 ? 90 * (peerSlots - 1) : 140
  const height = peerSlots > 2 ? 160 + spread : baseHeight
  const nodeX = large ? 155 : 142
  const nodeY = height / 2
  const positioned = online.slice(0, 5).map((device, index) => {
    const y =
      peerSlots === 1
        ? nodeY
        : nodeY - spread / 2 + (spread / (peerSlots - 1)) * index
    const x = large
      ? width - 305 - (index % 2) * 20
      : width - 228 - (index % 2) * 16
    return { device, x, y }
  })

  return (
    <div
      className={`topology-canvas ${large ? "topology-canvas-large" : ""}`}
      style={height > baseHeight ? { height: `${height}px` } : undefined}
    >
      <div
        className="topology-surface"
        role="img"
        aria-label={`Network topology. ${self.name} is this device. ${online.length} online devices and ${offline.length} offline devices.`}
      >
        <svg
          className="topology-lines"
          viewBox={`0 0 ${width} ${height}`}
          preserveAspectRatio="none"
          aria-hidden="true"
        >
          {positioned
            .filter(({ device }) => hasKnownConnection(device.connection))
            .map(({ device, x, y }) => {
              const isRelay = isRelayed(device.connection)
              return (
                <g
                  key={device.id}
                  className={
                    isRelay ? "link-group relay-link" : "link-group direct-link"
                  }
                >
                  <path
                    className="topology-link"
                    d={`M ${nodeX} ${nodeY} C ${nodeX + 110} ${nodeY}, ${x - 110} ${y}, ${x} ${y}`}
                  />
                </g>
              )
            })}
        </svg>
        <div
          className="topology-local"
          style={{
            left: `${(nodeX / width) * 100}%`,
            top: `${(nodeY / height) * 100}%`,
          }}
        >
          <div className="topology-local-halo">
            <div className="topology-node local-node">
              <Laptop size={large ? 25 : 21} strokeWidth={1.7} />
            </div>
          </div>
          <div className="topology-node-label local-node-label">
            <strong>{self.name}</strong>
            <span>
              <i />
              This device
            </span>
          </div>
        </div>
        {positioned.map(({ device, x, y }) => {
          const connectionType = connectionKind(device.connection)
          return (
            <div
              className={`topology-peer is-${connectionType}`}
              key={device.id}
              style={{
                left: `${(x / width) * 100}%`,
                top: `${(y / height) * 100}%`,
              }}
            >
              <div className="topology-peer-node">
                <DeviceGlyph platform={device.platform} />
                <span className="peer-node-dot" />
              </div>
              <div className="topology-node-label">
                <strong>{device.name}</strong>
                <span>
                  {connectionType !== "unknown" && (
                    <i
                      className={
                        connectionType === "relayed"
                          ? "relay-mark"
                          : "direct-mark"
                      }
                    />
                  )}
                  {connectionLabel(device.connection)}
                  {device.latencyMs > 0 ? ` · ${device.latencyMs} ms` : ""}
                </span>
              </div>
            </div>
          )
        })}
        {online.length === 0 && (
          <div className="topology-idle">
            <span>No connected devices</span>
          </div>
        )}
        {offline.length > 0 && (
          <div className="topology-offline">
            <span className="offline-dots">
              <i />
            </span>
            {offline.length} offline{" "}
            {offline.length === 1 ? "device" : "devices"}
          </div>
        )}
      </div>
    </div>
  )
}

function DeviceTableRow({
  device,
  onBlock,
}: {
  device: Device
  onBlock?: (device: Device) => void
}) {
  return (
    <tr>
      <td>
        <div className="table-primary">
          <span
            className={`row-icon device-row-icon ${device.self ? "self-row-icon" : ""}`}
          >
            <DeviceGlyph platform={device.platform} />
          </span>
          <span>
            <strong>
              {device.name}
              {device.self && <em className="you-tag">You</em>}
            </strong>
            <small>{device.platform || "Unknown platform"}</small>
          </span>
        </div>
      </td>
      <td>
        <span
          className={`status-dot-label ${device.online ? "is-online" : "is-offline"}`}
        >
          <i />
          {device.online ? "Online" : "Offline"}
        </span>
      </td>
      <td>
        <ConnectionLabel device={device} />
      </td>
      <td>
        <span className="mono-cell">{device.address || "—"}</span>
      </td>
      <td>{formatRelativeTime(device.lastSeen)}</td>
      <td className="td-actions">
        <div className="row-actions">
          {!device.self && onBlock && (
            <button
              type="button"
              className="icon-action danger-action"
              onClick={() => onBlock(device)}
              title="Block and revoke device"
              aria-label="Block device"
            >
              <Ban size={15} />
            </button>
          )}
        </div>
      </td>
    </tr>
  )
}

function DeviceRow({
  device,
  compact = false,
}: {
  device: Device
  compact?: boolean
}) {
  return (
    <div className="device-mini-row">
      <span
        className={`row-icon device-row-icon ${device.self ? "self-row-icon" : ""}`}
      >
        <DeviceGlyph platform={device.platform} />
      </span>
      <span className="mini-row-main">
        <strong>
          {device.name}
          {device.self && <em className="you-tag">You</em>}
        </strong>
        <small>
          {device.self
            ? "This device"
            : device.online
              ? formatConnection(device.connection)
              : "Last seen " + formatRelativeTime(device.lastSeen)}
        </small>
      </span>
      <span
        className={`status-orb ${device.online ? "is-online" : "is-offline"}`}
        title={device.online ? "Online" : "Offline"}
      />
      {!compact && device.latencyMs > 0 && (
        <span className="mini-latency">{device.latencyMs} ms</span>
      )}
    </div>
  )
}

function ServiceList({
  services,
  compact = false,
}: {
  services: Service[]
  compact?: boolean
}) {
  return (
    <div className="quiet-list">
      {services.map((service) => (
        <div className="quiet-list-row" key={service.id}>
          <span className="row-icon service-row-icon">
            <Globe2 size={15} />
          </span>
          <span className="quiet-row-main">
            <strong>{service.name}</strong>
            <small>
              {service.deviceName || "Unknown device"} ·{" "}
              {service.protocol.toUpperCase()}
              {!compact && ` · port ${service.port}`}
            </small>
          </span>
          <StatusLabel status={service.status} />
          <a
            className="icon-action quiet-list-action"
            href={service.url}
            target="_blank"
            rel="noreferrer"
            aria-label={`Open ${service.name}`}
          >
            <ArrowUpRight size={15} />
          </a>
        </div>
      ))}
    </div>
  )
}

function FolderList({
  folders,
  busyKey,
  onSync,
  onReviewConflicts,
  compact = false,
}: {
  folders: SharedFolder[]
  busyKey: string
  onSync: (folder: SharedFolder) => void
  onReviewConflicts?: (folder: SharedFolder) => void
  compact?: boolean
}) {
  return (
    <div className="quiet-list">
      {folders.map((folder) => (
        <div className="quiet-list-row folder-quiet-row" key={folder.id}>
          <span className="row-icon folder-row-icon">
            <Folder size={15} />
          </span>
          <span className="quiet-row-main">
            <strong>{folder.name}</strong>
            <small>
              {folder.error ||
                `${folder.peerName} · ${folder.files} files${!compact ? ` · ${formatBytes(folder.bytes)}` : ""}`}
            </small>
          </span>
          <span className="folder-state">
            <StatusLabel status={folderStatus(folder)} />
            {Boolean(
              folder.conflicts && folder.conflicts > 0 && onReviewConflicts
            ) && (
              <button
                type="button"
                className="conflict-badge-btn"
                onClick={() => onReviewConflicts(folder)}
                title="Review and resolve conflicts"
              >
                <AlertTriangle size={11} />
                <span>{folder.conflicts}</span>
              </button>
            )}
            {!compact && (
              <small>
                {folder.paused ? "Paused" : `${Math.round(folder.progress)}%`}
              </small>
            )}
          </span>
          <button
            className="icon-action quiet-list-action"
            onClick={() => onSync(folder)}
            disabled={busyKey === `sync-${folder.id}`}
            aria-label={`Sync ${folder.name}`}
            title="Sync now"
          >
            {busyKey === `sync-${folder.id}` ? (
              <LoaderCircle size={15} className="spin" />
            ) : (
              <RefreshCw size={15} />
            )}
          </button>
        </div>
      ))}
    </div>
  )
}

function ActivityList({
  activities,
  compact = false,
}: {
  activities: Activity[]
  compact?: boolean
}) {
  return (
    <div className={`activity-list ${compact ? "activity-list-compact" : ""}`}>
      {activities.map((activity) => (
        <div className="activity-row" key={activity.id}>
          <span className={`activity-icon activity-${activity.type}`}>
            <ActivityGlyph type={activity.type} />
          </span>
          <span className="activity-copy">
            <strong>{activity.title}</strong>
            <small>{activity.detail}</small>
          </span>
          <time dateTime={activity.time}>
            {formatRelativeTime(activity.time)}
          </time>
        </div>
      ))}
    </div>
  )
}

function ActivityGlyph({ type }: { type: string }) {
  if (type === "device") return <Laptop size={16} />
  if (type === "service") return <Globe2 size={16} />
  if (type === "sync") return <FolderSync size={16} />
  return <Radio size={16} />
}

function DeviceGlyph({ platform }: { platform: string }) {
  const value = platform.toLowerCase()
  return value.includes("linux") || value.includes("server") ? (
    <Server size={17} />
  ) : (
    <Laptop size={17} />
  )
}

function ConnectionLabel({ device }: { device: Device }) {
  if (device.self)
    return (
      <span className="connection-type">
        <i className="connection-mark local-mark" />
        Local
      </span>
    )
  if (!device.online)
    return <span className="connection-type muted-cell">—</span>
  const connection = connectionKind(device.connection)
  return (
    <span className="connection-type">
      {connection !== "unknown" && (
        <i
          className={`connection-mark ${connection === "relayed" ? "relay-mark" : "direct-mark"}`}
        />
      )}
      {connectionLabel(device.connection)}
      {device.latencyMs > 0 && <small>{device.latencyMs} ms</small>}
    </span>
  )
}

function isRelayed(connection: string) {
  return ["relay", "relayed", "circuit-relay", "circuit_relay"].includes(
    connection.toLowerCase()
  )
}

function isDirect(connection: string) {
  return connection.toLowerCase() === "direct"
}

function hasKnownConnection(connection: string) {
  return isRelayed(connection) || isDirect(connection)
}

function connectionKind(connection: string) {
  if (isRelayed(connection)) return "relayed"
  if (isDirect(connection)) return "direct"
  return "unknown"
}

function connectionLabel(connection: string) {
  if (isRelayed(connection)) return "Circuit relay"
  if (isDirect(connection)) return "Direct link"
  return connection ? humanizeStatus(connection) : "Connection unknown"
}

function formatConnection(connection: string) {
  if (isRelayed(connection)) return "Circuit relay"
  if (connection === "local") return "Local"
  if (!connection) return "Connection unknown"
  return humanizeStatus(connection)
}

function Stat({
  icon,
  label,
  value,
  suffix,
}: {
  icon: ReactNode
  label: string
  value: ReactNode
  suffix?: string
}) {
  return (
    <div className="stat-item">
      <span className="stat-icon">{icon}</span>
      <div className="stat-copy">
        <span>{label}</span>
        <strong>
          {value}
          {suffix && <small>{suffix}</small>}
        </strong>
      </div>
    </div>
  )
}

function PanelHeader({
  title,
  detail,
  icon,
  action,
}: {
  title: string
  detail?: string
  icon?: ReactNode
  action?: ReactNode
}) {
  return (
    <div className="panel-header">
      <div className="panel-title-wrap">
        {icon && <span className="panel-heading-icon">{icon}</span>}
        <div className="panel-heading-copy">
          <h2>{title}</h2>
          {detail && <span>{detail}</span>}
        </div>
      </div>
      {action && <div className="panel-header-action">{action}</div>}
    </div>
  )
}

function EmptyBlock({
  icon,
  title,
  detail,
  action,
}: {
  icon: ReactNode
  title: string
  detail?: string
  action?: ReactNode
}) {
  return (
    <div className="empty-block">
      <span className="empty-icon">{icon}</span>
      <strong>{title}</strong>
      {detail && <p>{detail}</p>}
      {action}
    </div>
  )
}

function StatusLabel({ status }: { status: string }) {
  const normal = status.toLowerCase()
  const good = [
    "healthy",
    "synced",
    "online",
    "published",
    "active",
    "complete",
    "completed",
  ].includes(normal)
  const progress = ["syncing", "connecting", "pending", "in_progress"].includes(
    normal
  )
  return (
    <span
      className={`status-label ${good ? "status-good" : progress ? "status-progress" : "status-neutral"}`}
    >
      <i />
      {humanizeStatus(status)}
    </span>
  )
}

function FolderProgress({ folder }: { folder: SharedFolder }) {
  if (folder.paused || folder.progress >= 100 || folder.progress <= 0)
    return null
  return (
    <div className="progress-wrap">
      <span>
        <i
          style={{ width: `${Math.min(100, Math.max(0, folder.progress))}%` }}
        />
      </span>
      <small>{Math.round(folder.progress)}%</small>
    </div>
  )
}

function folderStatus(folder: SharedFolder) {
  if (folder.error) return "error"
  return folder.paused ? "paused" : folder.status
}

function FormActions({
  onCancel,
  busy,
  submitLabel,
  disabled = false,
}: {
  onCancel: () => void
  busy: boolean
  submitLabel: string
  disabled?: boolean
}) {
  return (
    <div className="dialog-actions">
      <button
        type="button"
        className="button button-secondary"
        onClick={onCancel}
        disabled={busy}
      >
        Cancel
      </button>
      <button
        type="submit"
        className="button button-primary"
        disabled={busy || disabled}
      >
        {busy && <LoaderCircle size={15} className="spin" />}
        {busy ? "Working…" : submitLabel}
      </button>
    </div>
  )
}

function Dialog({
  open,
  title,
  onClose,
  children,
  busy = false,
  error,
  onDismissError,
  className = "",
}: {
  open: boolean
  title: string
  onClose: () => void
  children: ReactNode
  busy?: boolean
  error?: string
  onDismissError?: () => void
  className?: string
}) {
  const dialogRef = useRef<HTMLDialogElement>(null)
  const titleId = useId()
  useEffect(() => {
    const element = dialogRef.current
    if (!element) return
    if (open && !element.open) {
      element.showModal()
      element.querySelector<HTMLElement>("input, select, textarea")?.focus()
    }
    if (!open && element.open) element.close()
  }, [open])
  return (
    <dialog
      className={`app-dialog ${className}`.trim()}
      ref={dialogRef}
      aria-labelledby={titleId}
      onCancel={(event) => {
        if (busy) event.preventDefault()
      }}
      onClose={onClose}
      onClick={(event) => {
        if (!busy && event.target === event.currentTarget)
          event.currentTarget.close()
      }}
    >
      <div className="dialog-top">
        <div>
          <h2 id={titleId}>{title}</h2>
        </div>
        <button
          className="dialog-close"
          onClick={() => {
            if (!busy) dialogRef.current?.close()
          }}
          aria-label="Close dialog"
          disabled={busy}
        >
          <X size={18} />
        </button>
      </div>
      <div className="dialog-body">
        {error && (
          <div className="dialog-inline-error" role="alert">
            <CircleHelp size={15} />
            <span>{error}</span>
            {onDismissError && (
              <button
                type="button"
                aria-label="Dismiss error"
                onClick={onDismissError}
              >
                <X size={14} />
              </button>
            )}
          </div>
        )}
        {children}
      </div>
    </dialog>
  )
}

function ConflictsDialog({
  folder,
  onClose,
  onResolved,
}: {
  folder: SharedFolder | null
  onClose: () => void
  onResolved: () => void
}) {
  const [conflicts, setConflicts] = useState<Conflict[]>([])
  const [loading, setLoading] = useState(false)
  const [resolvingId, setResolvingId] = useState<string | null>(null)
  const [error, setError] = useState("")

  const loadConflicts = useCallback(async () => {
    if (!folder) return
    setLoading(true)
    setError("")
    try {
      const list = await getConflicts(folder.id)
      setConflicts(list || [])
    } catch (err) {
      setError(err instanceof Error ? err.message : "Failed to load conflicts")
    } finally {
      setLoading(false)
    }
  }, [folder])

  useEffect(() => {
    if (folder) {
      void loadConflicts()
    } else {
      setConflicts([])
      setError("")
      setResolvingId(null)
    }
  }, [folder, loadConflicts])

  const handleResolve = async (
    conflictId: string,
    resolution: "local" | "remote" | "both"
  ) => {
    if (!folder) return
    setResolvingId(conflictId)
    setError("")
    try {
      await resolveConflict(folder.id, conflictId, resolution)
      setConflicts((prev) => prev.filter((c) => c.id !== conflictId))
      onResolved()
    } catch (err) {
      setError(
        err instanceof Error ? err.message : "Failed to resolve conflict"
      )
    } finally {
      setResolvingId(null)
    }
  }

  return (
    <Dialog
      open={Boolean(folder)}
      title={folder ? `File conflicts: ${folder.name}` : "Conflicts"}
      onClose={onClose}
      className="dialog-wide"
      error={error}
      onDismissError={() => setError("")}
    >
      {loading ? (
        <div className="dialog-loading">
          <LoaderCircle size={18} className="spin" /> Checking conflicts…
        </div>
      ) : conflicts.length === 0 ? (
        <div className="conflicts-empty">
          <Check size={28} />
          <p>
            <strong>All clear!</strong>
          </p>
          <p className="field-help">
            There are no unresolved file conflicts in this folder.
          </p>
          <div className="dialog-actions" style={{ marginTop: "16px" }}>
            <button
              type="button"
              className="button button-primary"
              onClick={onClose}
            >
              Done
            </button>
          </div>
        </div>
      ) : (
        <div className="conflicts-list">
          <p className="conflicts-summary">
            Simultaneous edits occurred on multiple devices. Review the versions
            below and choose how to resolve each file:
          </p>
          {conflicts.map((c) => (
            <div key={c.id} className="conflict-card">
              <div className="conflict-header">
                <span className="conflict-file-path" title={c.path}>
                  <strong>{c.path}</strong>
                </span>
                <span className="conflict-device-badge">
                  Device: {c.deviceName || "Remote peer"}
                </span>
              </div>
              <div className="conflict-meta-split">
                <div className="conflict-version-col">
                  <span className="version-tag">Local copy</span>
                  <span className="version-info">
                    {formatBytes(c.localSize)} · Modified{" "}
                    {formatRelativeTime(c.localModTime)}
                  </span>
                </div>
                <div className="conflict-version-col">
                  <span className="version-tag">Remote conflict copy</span>
                  <span className="version-info">
                    {formatBytes(c.remoteSize)} · Modified{" "}
                    {formatRelativeTime(c.remoteModTime)}
                  </span>
                  <small
                    className="mono-cell conflict-copy-path"
                    title={c.conflictPath}
                  >
                    {c.conflictPath}
                  </small>
                </div>
              </div>
              <div className="conflict-actions">
                <button
                  type="button"
                  className="button button-secondary"
                  disabled={resolvingId === c.id}
                  onClick={() => void handleResolve(c.id, "local")}
                  title="Keep the local version and discard the remote conflict copy"
                >
                  Keep Local
                </button>
                <button
                  type="button"
                  className="button button-secondary"
                  disabled={resolvingId === c.id}
                  onClick={() => void handleResolve(c.id, "remote")}
                  title="Overwrite with the remote version and remove the conflict copy"
                >
                  Keep Remote
                </button>
                <button
                  type="button"
                  className="button button-primary"
                  disabled={resolvingId === c.id}
                  onClick={() => void handleResolve(c.id, "both")}
                  title="Keep both files (original and the .sync-conflict file) and mark resolved"
                >
                  Keep Both
                </button>
                {resolvingId === c.id && (
                  <LoaderCircle
                    size={15}
                    className="spin"
                    style={{ marginLeft: "8px" }}
                  />
                )}
              </div>
            </div>
          ))}
        </div>
      )}
    </Dialog>
  )
}

function LoadingView() {
  return (
    <div className="loading-shell">
      <div className="loading-card">
        <span className="loading-mark">
          <Link2 size={20} />
        </span>
        <strong>Connecting to Loom</strong>
        <span className="loading-track">
          <i />
        </span>
      </div>
    </div>
  )
}

function ApiUnavailable({
  message,
  onRetry,
}: {
  message: string
  onRetry: () => void
}) {
  return (
    <div className="unavailable-shell">
      <div className="unavailable-card">
        <span className="unavailable-icon">
          <WifiOff size={22} />
        </span>
        <h1>Loom API is unavailable</h1>
        <p>{message || "The local API did not return a network state."}</p>
        <div className="local-api-note">
          <strong>Start the Loom backend</strong>
          <code>
            go run ./cmd/loom --listen 127.0.0.1:8787 --data-dir .loom
          </code>
        </div>
        <button className="button button-primary" onClick={onRetry}>
          <RefreshCw size={15} /> Try again
        </button>
      </div>
    </div>
  )
}

function humanizeStatus(status: string) {
  if (!status) return "Unknown"
  return status
    .replaceAll("_", " ")
    .replace(/\b\w/g, (letter) => letter.toUpperCase())
}

function formatBytes(bytes: number) {
  if (!Number.isFinite(bytes) || bytes <= 0) return "0 B"
  const units = ["B", "KB", "MB", "GB", "TB"]
  const index = Math.min(
    Math.floor(Math.log(bytes) / Math.log(1024)),
    units.length - 1
  )
  const number = bytes / 1024 ** index
  return `${number.toLocaleString(undefined, { maximumFractionDigits: index === 0 ? 0 : 1 })} ${units[index]}`
}

function formatRelativeTime(value: string) {
  if (!value) return "—"
  const time = new Date(value).getTime()
  if (!Number.isFinite(time) || time <= 0) return "—"
  const seconds = Math.max(0, Math.floor((Date.now() - time) / 1000))
  if (seconds < 60) return "Just now"
  if (seconds < 3600) return `${Math.floor(seconds / 60)} min ago`
  if (seconds < 86400) return `${Math.floor(seconds / 3600)} hr ago`
  return `${Math.floor(seconds / 86400)} days ago`
}
