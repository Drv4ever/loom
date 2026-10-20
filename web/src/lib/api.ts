export type Network = {
  id: string
  name: string
  connected: boolean
  demo: boolean
  peerId: string
  addresses: string[]
  version: string
}

export type Device = {
  id: string
  name: string
  platform: string
  online: boolean
  self: boolean
  connection: string
  latencyMs: number
  address: string
  lastSeen: string
}

export type Service = {
  id: string
  name: string
  deviceId: string
  deviceName: string
  protocol: string
  target: string
  port: number
  status: string
  url: string
  trusted?: boolean
  allowedPeers?: string[]
}

export type Forward = {
  id: string
  peerId: string
  peerName?: string
  serviceId?: string
  target: string
  localPort: number
  status: string
  error?: string
}

export type Conflict = {
  id: string
  folderId: string
  path: string
  conflictPath: string
  localModTime: string
  remoteModTime: string
  localSize: number
  remoteSize: number
  deviceName: string
  createdAt: string
}

export type Folder = {
  id: string
  name: string
  path: string
  peerId: string
  peerName: string
  status: string
  files: number
  bytes: number
  progress: number
  lastSync: string
  paused: boolean
  error: string
  conflicts?: number
  allowedPeers?: string[]
  readOnly?: boolean
}

export type Activity = {
  id: string
  type: "device" | "service" | "sync" | "system" | string
  title: string
  detail: string
  time: string
}

export type ReceivedFolder = {
  id: string
  name: string
  path: string
  peerId: string
  peerName: string
  files: { path: string; size: number; sha256: string }[]
  bytes: number
  lastReceived: string
}

export type RelayInfo = {
  address: string
  peerId: string
  status: string
  error?: string
}

export type AppState = {
  network: Network
  devices: Device[]
  services: Service[]
  folders: Folder[]
  receivedFolders?: ReceivedFolder[]
  forwards?: Forward[]
  relays?: string[]
  blockedDevices?: string[]
  activity: Activity[]
  stats: {
    onlineDevices: number
    services: number
    syncedFiles: number
    transferredBytes: number
  }
}

export type PairingSession = {
  pin: string
  formatted: string
  topic: string
  expiresAt: string
  attempts?: number
}

export type PairingPayload = {
  networkKey: string
  networkId: string
  networkName: string
  issuedBy: string
  relayAddresses?: string[]
  peerAddresses?: string[]
  expiresAt: number
}

export type Diagnostics = Record<string, unknown>

export class ApiError extends Error {
  status: number

  constructor(message: string, status: number) {
    super(message)
    this.name = "ApiError"
    this.status = status
  }
}

export async function requestJson<T>(
  path: string,
  init?: RequestInit
): Promise<T> {
  const controller = new AbortController()
  const timeout = setTimeout(() => controller.abort(), 12_000)
  try {
    const response = await fetch(path, {
      ...init,
      signal: init?.signal ?? controller.signal,
      headers: {
        ...(init?.body ? { "Content-Type": "application/json" } : {}),
        ...init?.headers,
      },
    })
    const payload = await response.json().catch(() => null)

    if (!response.ok) {
      const message =
        payload && typeof payload.error === "string"
          ? payload.error
          : `Request failed (${response.status})`
      throw new ApiError(message, response.status)
    }

    return payload as T
  } finally {
    clearTimeout(timeout)
  }
}

export function postJson<T>(path: string, body: unknown): Promise<T> {
  return requestJson<T>(path, {
    method: "POST",
    body: JSON.stringify(body),
  })
}

export function deleteResource<T>(path: string): Promise<T> {
  return requestJson<T>(path, { method: "DELETE" })
}

export function getConflicts(folderId: string): Promise<Conflict[]> {
  return requestJson<Conflict[]>(`/api/folders/${encodeURIComponent(folderId)}/conflicts`)
}

export function resolveConflict(
  folderId: string,
  conflictId: string,
  resolution: "local" | "remote" | "both"
): Promise<{ ok: boolean }> {
  return postJson<{ ok: boolean }>(
    `/api/folders/${encodeURIComponent(folderId)}/resolve`,
    { conflictId, resolution }
  )
}

export function getForwards(): Promise<Forward[]> {
  return requestJson<Forward[]>("/api/forwards")
}

export function startForward(body: {
  peerId: string
  serviceId?: string
  target?: string
  localPort?: number
}): Promise<Forward> {
  return postJson<Forward>("/api/forwards", body)
}

export function stopForward(id: string): Promise<{ ok: boolean }> {
  return deleteResource<{ ok: boolean }>(`/api/forwards/${encodeURIComponent(id)}`)
}

export function getRelays(): Promise<RelayInfo[]> {
  return requestJson<RelayInfo[]>("/api/relays")
}

export function addRelay(address: string): Promise<RelayInfo> {
  return postJson<RelayInfo>("/api/relays", { address })
}

export function removeRelay(id: string): Promise<{ ok: boolean }> {
  return deleteResource<{ ok: boolean }>(`/api/relays/${encodeURIComponent(id)}`)
}

export function generatePairingCode(durationSeconds = 600): Promise<PairingSession> {
  return postJson<PairingSession>("/api/pairing/generate", { durationSeconds })
}

export function getPairingSession(): Promise<{ active: boolean; session?: PairingSession }> {
  return requestJson<{ active: boolean; session?: PairingSession }>("/api/pairing")
}

export function claimPairingCode(pin: string, relay?: string): Promise<PairingPayload> {
  return postJson<PairingPayload>("/api/pairing/claim", { pin, relay })
}

export function getBlockedDevices(): Promise<{ blocked: string[] }> {
  return requestJson<{ blocked: string[] }>("/api/devices/blocked")
}

export function blockDevice(id: string): Promise<{ ok: boolean }> {
  return postJson<{ ok: boolean }>(`/api/devices/${encodeURIComponent(id)}/block`, {})
}

export function unblockDevice(id: string): Promise<{ ok: boolean }> {
  return postJson<{ ok: boolean }>(`/api/devices/${encodeURIComponent(id)}/unblock`, {})
}
