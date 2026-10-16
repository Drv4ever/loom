import { createFileRoute } from "@tanstack/react-router"
import { LoomDashboard } from "@/components/loom-dashboard"

export const Route = createFileRoute("/")({
  component: LoomDashboard,
})
