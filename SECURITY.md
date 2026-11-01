# Security scope

Designed for trusted LAN use. Do not expose the HTTP listener directly to the Internet. HTTPS can be supplied by a separately configured reverse proxy. A NAS being on a private address does not itself prove absence of router forwarding, UPnP, tunnels or VPN access.

Application accounts are separate from DSM. Administrators can access all configured libraries and connection settings; viewers receive only selected roots. Signed image/subtitle links are bearer links tied to the current enabled account and library access. Password reset or disabling an account revokes login sessions. Do not put credentials or logs in public issues; protect state and rollback backups.

Native DLNA does not use this application's account permissions. Grant the MediaServer system account read-only media ACLs, and restrict DLNA using Synology and network controls.
