import {
  Activity as ActivityIcon,
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
  type Diagnostics,
  type Device,
  type Folder as SharedFolder,
  type Service,
  deleteResource,
  postJson,
  requestJson,
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
            />
          )}
          {page === "folders" && (
            <FoldersPage
              state={state}
              busyKey={busyKey}
              onAdd={() => openDialog("folder")}
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
            />
          )}
        </div>
      </main>

      <Dialog
        open={dialog === "device"}
        title="Add a device"
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
                postJson("/api/peers", {
                  address: String(data.get("address") || "").trim(),
                }),
              "Device connected"
            )
          }}
        >
          <label className="field-label" htmlFor="device-address">
            Multiaddress
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
            Paste the device’s full libp2p multiaddress. Both devices must use
            the same network key.
          </p>
          <FormActions
            onCancel={() => setDialog(null)}
            busy={busyKey === "dialog"}
            submitLabel="Connect device"
          />
        </form>
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
            void handleAction(
              event,
              () =>
                postJson("/api/services", {
                  name: String(data.get("name") || "").trim(),
                  target: String(data.get("target") || "").trim(),
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
          <p className="field-help">
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
            Enter an absolute path. The browser cannot browse folders on this
            device; files stay in their selected source folder.
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
    </div>
  )
}

function Overview({
  state,
  busyKey,
  onService,
  onFolder,
  onDemo,
  onPage,
  onSync,
}: {
  state: AppState
  busyKey: string
  onDevice: () => void
  onService: () => void
  onFolder: () => void
  onDemo: () => void
  onPage: (page: PageKey) => void
  onSync: (folder: SharedFolder) => void
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

function DevicesPage({
  state,
  onAdd,
  onCopy,
  copied,
}: {
  state: AppState
  onAdd: () => void
  onCopy: () => void
  copied: boolean
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
                </tr>
              </thead>
              <tbody>
                {state.devices.map((device) => (
                  <DeviceTableRow key={device.id} device={device} />
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
      </section>
    </>
  )
}

function ServicesPage({
  state,
  busyKey,
  onAdd,
  onRemove,
}: {
  state: AppState
  busyKey: string
  onAdd: () => void
  onRemove: (service: Service) => void
}) {
  return (
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
  )
}

function FoldersPage({
  state,
  busyKey,
  onAdd,
  onSync,
  onPause,
  onRemove,
}: {
  state: AppState
  busyKey: string
  onAdd: () => void
  onSync: (folder: SharedFolder) => void
  onPause: (folder: SharedFolder) => void
  onRemove: (folder: SharedFolder) => void
}) {
  return (
    <section className="panel data-panel">
      <PanelHeader title="Synced folders" icon={<FolderSync size={17} />} />
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
                        <small className="mono-cell">{folder.path}</small>
                      </span>
                    </div>
                  </td>
                  <td>{folder.peerName || "Unknown device"}</td>
                  <td>
                    <StatusLabel status={folderStatus(folder)} />
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
}: {
  state: AppState
  busyKey: string
  onToggleNetwork: () => void
  onDiagnostics: () => void
  onCopy: (address: string) => void
  copiedAddress: string
}) {
  const self = state.devices.find((device) => device.self)
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

function DeviceTableRow({ device }: { device: Device }) {
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
  compact = false,
}: {
  folders: SharedFolder[]
  busyKey: string
  onSync: (folder: SharedFolder) => void
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
}: {
  open: boolean
  title: string
  onClose: () => void
  children: ReactNode
  busy?: boolean
  error?: string
  onDismissError?: () => void
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
      className="app-dialog"
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
