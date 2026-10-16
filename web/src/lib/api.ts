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

export type AppState = {
  network: Network
  devices: Device[]
  services: Service[]
  folders: Folder[]
  receivedFolders?: ReceivedFolder[]
  activity: Activity[]
  stats: {
    onlineDevices: number
    services: number
    syncedFiles: number
    transferredBytes: number
  }
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
