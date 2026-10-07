import type { NextConfig } from "next"

const nextConfig: NextConfig = {
  // The Go server embeds and serves a fully static export.
  output: "export",
  trailingSlash: true,
  images: { unoptimized: true },
  // The app is served from the same origin as the Go API.
  reactStrictMode: true,
}

export default nextConfig
