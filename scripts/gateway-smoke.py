#!/usr/bin/env python3
"""Read-only smoke tests for a running Lnh-Music-Tag streaming gateway.

Usage:
  LNH_TEST_USER=admin LNH_TEST_PASS='your-password' \
    python3 scripts/gateway-smoke.py --base http://127.0.0.1:10248

The script avoids creating/deleting playlists, changing favorites, or writing tags.
Use HTTPS for remote hosts. Passwords are sent only to the server selected by --base.
"""
from __future__ import annotations

import argparse
import json
import os
import sys
import urllib.error
import urllib.parse
import urllib.request
from typing import Any


def parse_args() -> argparse.Namespace:
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument("--base", default="http://127.0.0.1:10248", help="server base URL")
    p.add_argument("--username", default=os.getenv("LNH_TEST_USER", "admin"))
    p.add_argument("--password", default=os.getenv("LNH_TEST_PASS", ""))
    p.add_argument("--jellyfin-prefix", default="", help="optional Jellyfin base prefix, e.g. /jellyfin")
    p.add_argument("--emby-prefix", default="/emby", help="Emby base prefix (default: /emby)")
    p.add_argument("--timeout", type=float, default=8.0)
    return p.parse_args()


class Smoke:
    def __init__(self, args: argparse.Namespace):
        self.args = args
        self.base = args.base.rstrip("/")
        self.results: list[tuple[str, str, str]] = []

    def request(self, name: str, method: str, path: str, *, query: dict[str, Any] | None = None,
                headers: dict[str, str] | None = None, body: Any = None,
                expected: tuple[int, ...] = (200,), max_read: int | None = None) -> tuple[int, bytes, dict[str, str]] | None:
        url = self.base + (path if path.startswith("/") else "/" + path)
        if query:
            url += ("&" if "?" in url else "?") + urllib.parse.urlencode(query, doseq=True)
        data = None
        h = {"Accept": "application/json", "User-Agent": "Lnh-Gateway-Smoke/1.0"}
        if headers:
            h.update(headers)
        if body is not None:
            data = body if isinstance(body, bytes) else json.dumps(body).encode("utf-8")
            h.setdefault("Content-Type", "application/json")
        req = urllib.request.Request(url, data=data, headers=h, method=method)
        try:
            response = urllib.request.urlopen(req, timeout=self.args.timeout)
            status = response.status
            payload = response.read(max_read) if max_read else response.read()
            response_headers = dict(response.headers.items())
            response.close()
        except urllib.error.HTTPError as e:
            status = e.code
            payload = e.read(max_read) if max_read else e.read()
            response_headers = dict(e.headers.items())
            e.close()
        except Exception as e:  # connection failures should be reported, not hidden
            self.results.append(("FAIL", name, f"request failed: {type(e).__name__}: {e}"))
            return None
        if status not in expected:
            self.results.append(("FAIL", name, f"HTTP {status}; expected {expected}; body={payload[:180]!r}"))
        else:
            self.results.append(("PASS", name, f"HTTP {status}"))
        return status, payload, response_headers

    def skip(self, name: str, reason: str) -> None:
        self.results.append(("SKIP", name, reason))

    def json_request(self, name: str, method: str, path: str, **kwargs: Any) -> tuple[int, Any, dict[str, str]] | None:
        result = self.request(name, method, path, **kwargs)
        if result is None:
            return None
        status, raw, headers = result
        try:
            value = json.loads(raw.decode("utf-8")) if raw else None
        except Exception as e:
            self.results.append(("FAIL", name + " JSON", f"invalid JSON response: {e}; body={raw[:180]!r}"))
            return None
        return status, value, headers

    def check_subsonic_payload(self, name: str, result: tuple[int, Any, dict[str, str]] | None) -> dict[str, Any] | None:
        if result is None:
            return None
        _, payload, _ = result
        root = payload.get("subsonic-response") if isinstance(payload, dict) else None
        if not isinstance(root, dict):
            self.results.append(("FAIL", name + " envelope", "missing subsonic-response object"))
            return None
        if root.get("status") != "ok":
            self.results.append(("FAIL", name + " API status", f"status={root.get('status')!r}, error={root.get('error')!r}"))
            return None
        self.results.append(("PASS", name + " API status", "status=ok"))
        return root

    @staticmethod
    def as_list(value: Any) -> list[Any]:
        if value is None:
            return []
        return value if isinstance(value, list) else [value]

    def run_public_probes(self) -> None:
        ping = self.json_request("Subsonic ping", "GET", "/rest/ping.view", query={"f": "json"})
        self.check_subsonic_payload("Subsonic ping", ping)
        ext = self.json_request("OpenSubsonic extensions", "GET", "/rest/getOpenSubsonicExtensions.view", query={"f": "json"})
        self.check_subsonic_payload("OpenSubsonic extensions", ext)
        unauth = self.json_request("Subsonic rejects unauthenticated library read", "GET", "/rest/getMusicFolders.view", query={"f": "json"})
        if unauth and isinstance(unauth[1], dict):
            root = unauth[1].get("subsonic-response", {})
            if root.get("status") == "failed":
                self.results.append(("PASS", "Subsonic auth gate", "unauthenticated library read was rejected"))
            else:
                self.results.append(("FAIL", "Subsonic auth gate", "library data endpoint did not reject unauthenticated request"))
        jf = self.json_request("Jellyfin public server info", "GET", self.args.jellyfin_prefix.rstrip("/") + "/System/Info/Public")
        if jf and not isinstance(jf[1], dict):
            self.results.append(("FAIL", "Jellyfin info JSON", "expected JSON object"))
        emby_path = self.args.emby_prefix.rstrip("/") + "/System/Info/Public"
        emb = self.json_request("Emby public server info", "GET", emby_path)
        if emb and not isinstance(emb[1], dict):
            self.results.append(("FAIL", "Emby info JSON", "expected JSON object"))
        nd = self.json_request("Navidrome-style ping", "GET", "/ping")
        if nd and (not isinstance(nd[1], dict) or nd[1].get("status") != "ok"):
            self.results.append(("FAIL", "Navidrome ping body", "expected JSON with status=ok"))

    def run_authenticated(self) -> None:
        if not self.args.password:
            self.skip("Authenticated protocol probes", "set LNH_TEST_PASS or --password to enable login/stream tests")
            return
        user, password = self.args.username, self.args.password

        # Subsonic/OpenSubsonic: use a legacy password query only against the configured test server.
        sub_auth = {"u": user, "p": password, "v": "1.16.1", "c": "lnh-gateway-smoke", "f": "json"}
        roots: dict[str, dict[str, Any]] = {}
        for op in ("getMusicFolders", "getArtists", "getGenres", "getAlbumList2", "search3", "getPlaylists", "getStarred2", "getScanStatus"):
            q = dict(sub_auth)
            if op == "getAlbumList2": q.update({"type": "alphabeticalByName", "size": 5})
            if op == "search3": q["query"] = "a"
            result = self.json_request("Subsonic " + op, "GET", f"/rest/{op}.view", query=q)
            root = self.check_subsonic_payload("Subsonic " + op, result)
            if root:
                roots[op] = root
        song_candidates: list[dict[str, Any]] = []
        for root in (roots.get("search3", {}),):
            result = root.get("searchResult3", {})
            song_candidates.extend(x for x in self.as_list(result.get("song")) if isinstance(x, dict) and x.get("id"))
        if not song_candidates:
            result = self.json_request("Subsonic getRandomSongs", "GET", "/rest/getRandomSongs.view", query={**sub_auth, "count": 5})
            root = self.check_subsonic_payload("Subsonic getRandomSongs", result)
            if root:
                song_candidates.extend(x for x in self.as_list(root.get("randomSongs", {}).get("song")) if isinstance(x, dict) and x.get("id"))
        if song_candidates:
            song = song_candidates[0]
            sid = str(song["id"])
            q = {**sub_auth, "id": sid}
            stream = self.request("Subsonic byte-range audio stream", "GET", "/rest/stream.view", query=q,
                                  headers={"Range": "bytes=0-1023"}, expected=(206,), max_read=1024)
            if stream and not stream[1]:
                self.results.append(("FAIL", "Subsonic range body", "range response had an empty body"))
            lyr = self.json_request("Subsonic lyrics by song id", "GET", "/rest/getLyricsBySongId.view", query=q)
            self.check_subsonic_payload("Subsonic lyrics by song id", lyr)
            cover_id = song.get("coverArt") or sid
            cov = self.request("Subsonic cover art", "GET", "/rest/getCoverArt.view", query={**sub_auth, "id": cover_id}, expected=(200, 404), max_read=64)
            if cov and cov[0] == 404:
                self.skip("Subsonic cover art content", "test track has no readable embedded cover (HTTP 404 is valid in that case)")
        else:
            self.skip("Subsonic audio/cover/lyrics with track ID", "search returned no songs; verify the server has scanned music")

        # Navidrome-style legacy REST routes; this is not the separate /api/v1 OpenAPI API.
        nd_login = self.json_request("Navidrome-style login", "POST", "/auth/login", body={"username": user, "password": password})
        nd_token = None
        if nd_login and isinstance(nd_login[1], dict):
            nd_token = nd_login[1].get("token")
            if not nd_token:
                self.results.append(("FAIL", "Navidrome-style login token", "login body did not contain token"))
        if nd_token:
            nh = {"Authorization": "Bearer " + str(nd_token)}
            nd_songs = self.json_request("Navidrome-style song list", "GET", "/api/song", query={"limit": 5}, headers=nh)
            if nd_songs and not isinstance(nd_songs[1], list):
                self.results.append(("FAIL", "Navidrome song list schema", "expected a JSON array"))
            self.json_request("Navidrome-style album list", "GET", "/api/album", query={"limit": 5}, headers=nh)
            self.json_request("Navidrome-style artist list", "GET", "/api/artist", query={"limit": 5}, headers=nh)
            self.json_request("Navidrome-style playlist list", "GET", "/api/playlist", headers=nh)
            self.json_request("Navidrome-style search", "GET", "/api/search", query={"q": "a"}, headers=nh)
            if nd_songs and isinstance(nd_songs[1], list) and nd_songs[1] and isinstance(nd_songs[1][0], dict):
                sid = str(nd_songs[1][0].get("id", ""))
                if sid:
                    self.request("Navidrome-style byte-range stream", "GET", f"/api/stream/{urllib.parse.quote(sid, safe='')}",
                                 headers={**nh, "Range": "bytes=0-1023"}, expected=(206,), max_read=1024)
            else:
                self.skip("Navidrome audio range", "song list is empty")

        # Shared login DTO contract is tested separately against Jellyfin and Emby paths.
        self.test_media_server("Jellyfin", self.args.jellyfin_prefix.rstrip("/"), user, password)
        self.test_media_server("Emby", self.args.emby_prefix.rstrip("/"), user, password)

    def test_media_server(self, name: str, prefix: str, user: str, password: str) -> None:
        auth = self.json_request(name + " login", "POST", prefix + "/Users/AuthenticateByName",
                                 body={"Username": user, "Pw": password})
        if not auth or not isinstance(auth[1], dict):
            return
        token = auth[1].get("AccessToken")
        user_data = auth[1].get("User", {})
        uid = user_data.get("Id", "lnh-admin") if isinstance(user_data, dict) else "lnh-admin"
        if not token:
            self.results.append(("FAIL", name + " login token", "login response has no AccessToken"))
            return
        headers = {"X-Emby-Token": str(token)}
        query = {"ParentId": uid, "IncludeItemTypes": "Audio", "Limit": 5}
        listing = self.json_request(name + " audio item list", "GET", prefix + "/Users/" + urllib.parse.quote(str(uid), safe="") + "/Items", query=query, headers=headers)
        if not listing or not isinstance(listing[1], dict):
            self.results.append(("FAIL", name + " Items schema", "expected an ItemQueryResult JSON object"))
            return
        items = listing[1].get("Items", [])
        if not isinstance(items, list):
            self.results.append(("FAIL", name + " Items array", "Items field is not an array"))
            return
        if not items:
            self.skip(name + " audio stream", "audio item list is empty")
            return
        sid = str(items[0].get("Id", ""))
        if not sid:
            self.results.append(("FAIL", name + " item ID", "first item has no Id"))
            return
        if name == "Jellyfin":
            self.json_request(name + " playback info", "GET", prefix + "/Items/" + urllib.parse.quote(sid, safe="") + "/PlaybackInfo", headers=headers)
            stream_path = prefix + "/Audio/" + urllib.parse.quote(sid, safe="") + "/universal"
        else:
            stream_path = prefix + "/Items/" + urllib.parse.quote(sid, safe="") + "/File"
        self.request(name + " byte-range audio stream", "GET", stream_path, headers={**headers, "Range": "bytes=0-1023"}, expected=(206,), max_read=1024)

    def report(self) -> int:
        for status, name, detail in self.results:
            print(f"[{status}] {name}: {detail}")
        counts = {s: sum(1 for status, _, _ in self.results if status == s) for s in ("PASS", "FAIL", "SKIP")}
        print(f"\nSummary: {counts['PASS']} PASS, {counts['FAIL']} FAIL, {counts['SKIP']} SKIP")
        print("Note: HTTP smoke tests validate gateway contracts, not real native-player compatibility.")
        return 1 if counts["FAIL"] else 0


def main() -> int:
    args = parse_args()
    if not args.base.startswith(("http://", "https://")):
        print("--base must start with http:// or https://", file=sys.stderr)
        return 2
    smoke = Smoke(args)
    smoke.run_public_probes()
    smoke.run_authenticated()
    return smoke.report()


if __name__ == "__main__":
    raise SystemExit(main())
