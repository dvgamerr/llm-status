# llm-status

Native pixel dashboard สำหรับดู Claude Code และ Codex usage บน Raspberry Pi 4 โดยรับ
ข้อมูลจาก Claude `statusLine` หรือ Codex turn notification + local rollout metadata
แล้วสร้าง sanitized snapshot ไม่ scrape หน้าเว็บและไม่อ่าน/ส่ง credential ของ provider

หน้าหลักวาดลง `/dev/fb0` แบบ RGB565 ที่ 800×480 โดยตรง ไม่ใช้ terminal grid,
Desktop, Chromium หรือ X/Wayland จึงควบคุม typography, spacing, สี และ rounded cards
ได้ทุกพิกเซล

## หน้าตาปัจจุบันของ dashboard

- ซ้าย: rail มี mascot (Clawd) เป็นจุดสนใจหลัก ใต้ mascot มีแค่ระยะเวลา
  ("working for 12s") — ไม่แสดงชื่อหรือ ID ของ session
  เลย ต่อด้วย Pi health (CPU/MEM/GPU)
- ขวาบน: "5 HOUR LIMIT" และ "7 DAY LIMIT" วางเต็มความกว้างซ้อนกันคนละแถว
  (label+เวลารีเซ็ตแถวเดียวกัน ตามด้วย bar แล้วเลขเปอร์เซ็นต์ใหญ่)
- กลาง: หัวข้อ model + reasoning level (เช่น "SONNET 5" / "HIGH EFFORT") สไตล์
  เดียวกับการ์ด Codex ตามด้วย "CONTEXT WINDOW" และ token chip INPUT/OUTPUT
- ขวาล่าง: การ์ด Codex (model, reasoning effort, context) ทั้งการ์ด Codex และ
  chip INPUT/OUTPUT ชิดขอบล่างของจอเป็นแนวเดียวกัน ไม่มีที่ว่างเหลือด้านล่าง
- header มีแค่ Anthropic mark กับคำว่า CLAUDE เท่านั้น ไม่มีนาฬิกา ไม่มี
  model/session identity (ข้อมูลนั้นย้ายไปอยู่กลางจอแทน)

## สิ่งที่โปรแกรมทำ

- `llm-status ingest` อ่าน Claude JSON จาก stdin, sanitize, เขียน state แบบ atomic
  แล้วพิมพ์ status line สั้นกลับให้ Claude Code
- `llm-status activity` อ่าน Claude Code hook event (`UserPromptSubmit`,
  `PreToolUse`, `Stop`, `Notification`, `SubagentStart`, `SubagentStop`) จาก stdin แล้วอัปเดตสถานะ
  working/idle/waiting-approval ของ session นั้น โดยไม่แตะ field อื่นและไม่เก็บ
  ข้อความ hook ดิบไว้เลย ใช้ขับ animation ของ mascot บนหน้าจอ
- `llm-status usage --five-hour PCT --seven-day PCT` เขียนแค่ 5h/7d limit
  ทับ session ล่าสุด (หรือ `--session ID` เจาะจง) โดยไม่แตะ field อื่น — ใช้ตอน
  `statusLine` ไม่ยิงจริง (เท่าที่เจอ ยิงได้แค่จาก CLI terminal ไม่ยิงจาก
  Claude Code แบบ VS Code extension) เอาไว้กรอกเลขจากหน้า Account & Usage มือ
- `llm-status codex-notify` รับ Codex turn-complete notification แล้วอ่านเฉพาะ
  model, context/token usage และ 5-hour/7-day usage จาก rollout ของ thread นั้น
- `llm-status import` รับเฉพาะ sanitized snapshot schema สำหรับเครื่อง Pi
- `llm-status relay` เป็น process แยกที่อ่าน snapshot ล่าสุดจาก local state,
  ส่งไป Pi ผ่าน SSH และ retry อัตโนมัติเมื่อเครือข่ายขาด
- `llm-status service install|remove|start|stop|status` ติดตั้ง relay ให้รัน
  เป็น background service — คำสั่งเดียวกันทำงานเหมือนกันบน Windows (Windows
  Service จริง), Linux (`systemd --user`) และ macOS (launchd `LaunchAgent`)
- `llm-status pi install` เขียนและเปิดใช้งาน systemd unit ของ dashboard บน
  Raspberry Pi เอง ชี้ไปที่ path ของ binary ปัจจุบันโดยตรง (รองรับ `go install`
  โดยไม่ต้อง clone repo)
- `llm-status gfx` เปิด native framebuffer dashboard 800×480, animate ที่ 66ms (~15fps)
  และเลือก snapshot ล่าสุดของ Claude/Codex แยกกันเพื่อให้ Claude เป็นหน้าหลักเสมอ
  จอเป็น touchscreen จริง แตะแล้วจะเห็น ripple จาง ๆ ตรงจุดที่แตะ (`--touch-device`
  ปรับ evdev device ได้ ใส่ค่าว่างเพื่อปิด) เป็น feedback อย่างเดียว ไม่ใช่ปุ่มกด
- `llm-status preview` render frame เดียวกันเป็น PNG สำหรับ visual QA
- quota ที่ provider ไม่ส่งจะแสดง unavailable; Codex account ที่ระบุ unlimited
  จะแสดง `UNMETERED` แทนการสร้างเปอร์เซ็นต์ขึ้นเอง
- `llm-status tui` ยังเก็บไว้เป็น fallback สำหรับเครื่องที่ไม่มี framebuffer
- แสดง `LIVE`/`STALE` ชัดเจน ป้องกันการเข้าใจ snapshot เก่าว่าเป็นข้อมูลสด
- รองรับ field ที่หาย, เป็น `null` และ field ใหม่ที่โปรแกรมยังไม่รู้จัก

## สถาปัตยกรรม: ข้อมูลเดินทางยังไงตั้งแต่ต้นจนขึ้นจอ

ระบบแยกเป็น 3 ชั้นที่ไม่พึ่งพากันโดยตรง ชั้นไหนพังก็ไม่ทำให้ชั้นอื่นค้าง:

1. **Source adapter** (เครื่องที่รัน Claude Code/Codex จริง) — `ingest`,
   `activity`, `usage`, `codex-notify` แต่ละตัวเป็น process สั้น ๆ ที่ถูกเรียก
   ต่อ event เดียว (statusLine refresh, hook, หรือ turn-complete notify) อ่าน
   input, sanitize ผ่าน allowlist แล้ว **เขียนแค่ local state แบบ atomic**
   (temp file + fsync + rename) จบแล้วก็ออก — ไม่เปิด network เองและไม่รอ SSH;
   เวลาที่ใช้มีเฉพาะการ decode และเขียนไฟล์ local ที่จำเป็น
2. **Relay** (`llm-status relay`) — process ระยะยาวตัวเดียวที่ watch local
   state directory เดียวกันนั้น เจอ snapshot ที่เปลี่ยน (เทียบ fingerprint SHA-256
   ของ sanitized content)
   ก็ส่งไป Pi ผ่าน SSH โดย retry เองเมื่อเครือข่ายขาดหรือ Pi ปิดอยู่ชั่วคราว
   เป็นเจ้าของ SSH transport เพียงจุดเดียวในทั้งระบบ ปลายทางเสมอคือ
   `llm-status import` บน Pi ซึ่งรับเฉพาะ `model.Snapshot` schema ที่รู้จัก
   และ reject field แปลกปลอมทันที
3. **Renderer** (`llm-status gfx` บน Pi) — loop เดียวที่อ่าน state
   directory ของตัวเอง (ที่ `import` เขียนไว้), เลือก snapshot ล่าสุดของ
   Claude กับ Codex แยกกัน (Claude เป็นหลักเสมอ ต่อให้ Codex event ใหม่กว่า),
   แล้ว composite เฟรมด้วย `internal/pixelui` วาดตรงลง `/dev/fb0` ทุก
   `--refresh` (ดีฟอลต์ 66ms/~15fps) — ไม่รอ SSH, ไม่รอ relay, ไม่ block

Activity state (working/idle/waiting-approval และจำนวน subagent) เดินคนละ path จาก
snapshot ทั่วไป: hook เขียนแค่ field `Activity{State, UpdatedAt, Subagents}` merge เข้า
ไปในของเดิม ไม่ทำให้ statusLine event ที่มาก่อน/หลังกันมาทับข้อมูลกัน และ
mascot จะ fallback กลับ idle เองถ้า state ค้างเกิน 10 นาที (เผื่อ `Stop`
hook หลุดไป)

ถ้าเครื่องรัน Claude Code/Codex เป็นเครื่องเดียวกับที่มี `/dev/fb0` (Pi ตัวเดียว
ทำหมด) ก็ข้าม relay ไปเลยได้ — `ingest`/`activity`/`gfx` อ่านเขียน state
directory เดียวกันตรง ๆ

## ติดตั้งบน Raspberry Pi

ต้องใช้ Raspberry Pi OS 64-bit ตัว dashboard ใช้งานบน Pi 4 RAM 2 GB ได้ แต่ถ้าจะรัน
Claude Code บน Pi เครื่องเดียวกัน ควรใช้ RAM 4 GB หรือ 8 GB ตามข้อกำหนดของ Claude Code

วิธีที่ง่ายที่สุด: ติดตั้งผ่าน `go install` (ต้องมี Go 1.25+ บน Pi) แล้วสั่ง
`pi install` ตัวเดียวจบ — ไม่ต้อง clone repo, ไม่ต้องเขียน systemd unit เอง:

```bash
uname -m                         # ควรได้ aarch64
go install github.com/dvgamerr/llm-status/cmd/llm-status@latest
sudo $(go env GOPATH)/bin/llm-status pi install
```

`pi install` เขียน systemd unit `llm-status-tty1.service` ชี้ไปที่ path ของ
binary ปัจจุบันโดยตรง (ไม่ hardcode `/home/pi/.local/bin` เหมือนเดิมอีกต่อไป)
ตั้ง `User`/`Group`/`WorkingDirectory` ตามผู้ใช้ที่เรียก (`$SUDO_USER`, override ได้
ด้วย `--user NAME`) แล้ว `systemctl daemon-reload` + `enable --now` ให้ในคำสั่งเดียว
— ต้องรันด้วยสิทธิ์ root (จะ `sudo` ให้เองอัตโนมัติถ้ายังไม่ได้รันแบบ root) ตัวเลือกอื่น:
`--refresh 66ms`, `--framebuffer /dev/fb0`, `--tty /dev/tty1`, `--touch-device
/dev/input/event0` (ใส่ค่าว่างเพื่อปิด touch feedback)

ถ้าไม่อยากพึ่ง Go module proxy ก็ยังใช้วิธี clone + build เองได้เหมือนเดิม:

```bash
git clone https://github.com/dvgamerr/llm-status
cd llm-status
bash scripts/install.sh          # หรือ: bash scripts/install.sh ./llm-status (มี binary cross-build แล้ว)
sudo ~/.local/bin/llm-status pi install
```

จากนั้นเพิ่มใน `~/.claude/settings.json` (หรือ project settings) ถ้า Claude Code
รันบน Pi เครื่องเดียวกัน:

```json
{
  "statusLine": {
    "type": "command",
    "command": "~/.local/bin/llm-status ingest",
    "padding": 1,
    "refreshInterval": 5
  },
  "hooks": {
    "UserPromptSubmit": [{ "hooks": [{ "type": "command", "command": "~/.local/bin/llm-status activity" }] }],
    "PreToolUse": [{ "matcher": "*", "hooks": [{ "type": "command", "command": "~/.local/bin/llm-status activity" }] }],
    "Stop": [{ "hooks": [{ "type": "command", "command": "~/.local/bin/llm-status activity" }] }],
    "Notification": [{ "hooks": [{ "type": "command", "command": "~/.local/bin/llm-status activity" }] }],
    "SubagentStart": [{ "hooks": [{ "type": "command", "command": "~/.local/bin/llm-status activity" }] }],
    "SubagentStop": [{ "hooks": [{ "type": "command", "command": "~/.local/bin/llm-status activity" }] }]
  }
}
```

hook ทั้งหกทำให้ mascot บนจอรู้สถานะจริงของ session: `UserPromptSubmit`/
`PreToolUse` → กำลังทำงาน (Clawd Coding พิมพ์อยู่ ตากะพริบ), `Stop` → idle
(Clawd Sleeping หายใจช้า ๆ พร้อม Zzz ลอย), `Notification` ที่มีคำว่า permission →
รอ approval (Clawd Exclamation Mark สั่นเตือน จุดตกใจกะพริบ) ส่วน
`SubagentStart`/`SubagentStop` ขับท่าของ subagent ตามจำนวนที่กำลังรัน แต่ละสถานะ
เล่นลำดับ SVG frames ของตัวเอง ส่วน rail และ card อยู่นิ่งทั้งหมด ถ้าไม่ตั้ง hook
พวกนี้ dashboard จะยังทำงานได้ปกติ แต่จะเดาสถานะจาก statusLine freshness แทน

ถ้า Claude Code/Codex รันอยู่คนละเครื่องกับจอ Pi ดูหัวข้อ "Claude/Codex อยู่บน
PC/Mac แต่ใช้ Pi เป็นจอ" ด้านล่าง สำหรับวิธีตั้งค่าฝั่งเครื่องต้นทาง

Claude Code ต้องได้รับ trust สำหรับ project ก่อนจึงจะเรียก command status line ได้
ค่า `rate_limits` จะปรากฏเฉพาะบัญชี Claude.ai Pro/Max และหลัง API response แรกของ
session เท่านั้น

ตั้งแต่ Claude Code v2.1.132 ค่า `total_input_tokens` และ `total_output_tokens`
หมายถึง token ใน context ปัจจุบันจาก API response ล่าสุด; รุ่นก่อนหน้านั้นเป็นยอดสะสม
ของ session

เปิด dashboard:

```bash
llm-status gfx --framebuffer /dev/fb0 --tty /dev/tty1
```

ตัวเลือกสำคัญ:

```text
--state-dir DIR       เปลี่ยนที่เก็บ snapshot
--refresh 66ms        รอบ animation, อ่านข้อมูล และ Pi metrics (ค่าเริ่มต้น ~15fps, ต่ำสุด 20ms)
--stale-after 15s     อายุข้อมูลก่อนแสดง STALE
--framebuffer PATH    framebuffer device; ค่าเริ่มต้น /dev/fb0
--tty PATH            console ที่สลับเข้า graphics mode; ค่าเริ่มต้น /dev/tty1
```

ตั้ง path ด้วย environment variable ได้เช่นกัน:

```bash
export LLM_STATUS_STATE_DIR=/var/lib/llm-status
```

## ทดลองด้วย mock data

```bash
llm-status ingest < examples/statusline-input.json
llm-status preview --output dashboard.png
```

ใน Nushell ใช้ `open --raw` และ external-command marker `^`:

```nu
open --raw examples/statusline-input.json | ^go run ./cmd/llm-status ingest
```

การรัน `llm-status ingest` เปล่า ๆ ไม่มี stdin จะ error ตามตั้งใจ เพราะตอนใช้งานจริง
Claude Code เป็นผู้ pipe JSON เข้ามาให้อัตโนมัติ

state จะอยู่ที่ `${XDG_CACHE_HOME:-~/.cache}/llm-status/` บน Linux:

```text
llm-status/
├── sessions/
│   └── <sha256-prefix>.json
└── latest.json
```

directory ใช้ permission `0700`; snapshot ใช้ `0600`; การเขียนใช้ temporary file,
`fsync` และ atomic rename

## Privacy และ security

โปรแกรมสร้าง struct ใหม่จาก allowlist เท่านั้น ไม่ serialize input ดิบ จึงไม่เก็บ:

- `transcript_path`
- prompt หรือข้อความสนทนา
- `~/.claude/.credentials.json`
- OAuth token หรือ API key
- path ของ workspace

ฝั่ง Codex จะเปิด local rollout ของ `thread-id` ที่ notify ส่งมา แต่ decode เฉพาะ
`session_meta`, `turn_context` และ `token_count`; บรรทัดอื่นรวมถึงข้อความสนทนาจะถูกข้าม
ทั้งหมด จากนั้นจึง serialize เฉพาะ `Snapshot` schema เดียวกับ Claude

ชื่อไฟล์ session เป็น hash เพื่อไม่ให้ `session_id` กลายเป็น path traversal หรือรั่วใน
directory listing ข้อมูล cost เป็นค่าประมาณจาก token ไม่ใช่ยอดเรียกเก็บจริง

## Claude/Codex อยู่บน PC/Mac แต่ใช้ Pi เป็นจอ

`ingest`/`activity`/`usage`/`codex-notify` เขียนแค่ local state บนเครื่องต้นทาง
และไม่เปิด network เอง — ตัว **relay** เป็น process ระยะยาวตัวเดียวที่เป็นเจ้าของ
SSH transport ทั้งหมด คอย watch local state แล้วส่งเฉพาะ snapshot ที่ sanitize
แล้วไปเรียก `/home/pi/.local/bin/llm-status import` บน Pi ห้าม copy credential
หรือส่ง JSON ดิบจาก provider ไปยัง Pi เด็ดขาด

ติดตั้ง relay ให้รันเป็น background service — คำสั่งเดียวกัน ทำงานเหมือนกันทั้ง
Windows, Linux, และ macOS:

```bash
go install github.com/dvgamerr/llm-status/cmd/llm-status@latest
llm-status service install --mirror-ssh pilab
```

`service install` ใช้ service manager ของแต่ละ OS โดยตรง (ไม่ผ่านตัวกลางอื่น):

| OS      | กลไกเบื้องหลัง                        | สิทธิ์ที่ต้องใช้                  |
|---------|----------------------------------------|-----------------------------------|
| Windows | Windows Service จริง (SCM)             | รันจาก Administrator shell        |
| Linux   | `systemd --user` unit                  | ไม่ต้อง root                      |
| macOS   | launchd `LaunchAgent`                  | ไม่ต้อง root                      |

คำสั่งอื่นในกลุ่มเดียวกัน: `llm-status service status|start|stop|remove`
(ใช้ชื่อบริการ `llm-status-relay` เดียวกันทุก OS) รัน `install` ซ้ำได้เสมอ —
ถ้ามี instance เดิมรันอยู่จะ stop แล้ว restart ให้เองด้วย binary/flag ใหม่

บน Windows ยังมีสคริปต์ `install-windows.ps1` ที่ทำครบในคำสั่งเดียว (ติดตั้ง
binary ที่ `-BinaryPath` ระบุหรือ `bin/llm-status.exe` + เพิ่ม hook ใน `~/.claude/settings.json` + wrap `notify` ใน
`~/.codex/config.toml` + เรียก `service install` ให้ตอนท้าย):

```powershell
pwsh -File scripts/verify.ps1
pwsh -File scripts/install-windows.ps1 -MirrorHost pilab
```

installer จะสำรองไฟล์เดิมไว้ก่อนแก้เสมอ (`.llm-status-backup-<timestamp>`)
บน Linux/macOS ยังต้องแก้ `~/.claude/settings.json` และ `~/.codex/config.toml`
ตามตัวอย่าง JSON ด้านบนเอง ก่อนรัน `service install`

### Windows: relay service รันเป็น LocalSystem — ต้อง seed SSH key ของ SYSTEM เอง

`service install` บน Windows สร้าง Windows Service จริงผ่าน SCM ซึ่งรันด้วยบัญชี
**LocalSystem** โดย default ไม่ใช่บัญชี user ที่สั่ง install — LocalSystem มี profile
แยกต่างหาก (`%windir%\System32\config\systemprofile`) และ**ไม่มี** `.ssh` ของตัวเอง
ต่อให้ `ssh pilab` จาก shell ของคุณเองใช้ได้ปกติ (มี key/`known_hosts`/`~/.ssh/config`
อยู่แล้ว) service ก็ยังต่อ SSH ไม่ได้ เพราะ LocalSystem มองไม่เห็นไฟล์พวกนั้นเลย
อาการที่เจอคือ `relay.log` ขึ้น `mirror failed` วนซ้ำด้วย
`Host key verification failed.` หรือ `Connection closed by ... port 22`

วิธีแก้คือ copy key + config entry + known_hosts เฉพาะ host ปลายทางไปไว้ใน profile
ของ SYSTEM แล้วล็อก ACL ให้เหลือแค่ SYSTEM/Administrators (ต้องรันจาก
Administrator shell):

```powershell
$sshDir = "$env:windir\System32\config\systemprofile\.ssh"
New-Item -ItemType Directory -Force -Path $sshDir | Out-Null

# แก้ HostName/User/IdentityFile ให้ตรงกับ ~/.ssh/config ของคุณเอง
@'
Host pilab
    HostName 10.203.1.159
    User pi
    IdentityFile ~/.ssh/id_pilab
    IdentitiesOnly yes
'@ | Set-Content -Path "$sshDir\config" -Encoding ascii -NoNewline

Copy-Item "$env:USERPROFILE\.ssh\id_pilab"     "$sshDir\id_pilab" -Force
Copy-Item "$env:USERPROFILE\.ssh\id_pilab.pub" "$sshDir\id_pilab.pub" -Force
Select-String -Path "$env:USERPROFILE\.ssh\known_hosts" -Pattern 'pilab' |
    ForEach-Object { $_.Line } | Set-Content -Path "$sshDir\known_hosts" -Encoding ascii

# OpenSSH ปฏิเสธ private key ที่ ACL กว้างกว่าเจ้าของ (เหมือนเช็ค 600 บน POSIX)
icacls $sshDir /inheritance:r /grant:r "SYSTEM:(OI)(CI)F" "BUILTIN\Administrators:(OI)(CI)F" | Out-Null
icacls "$sshDir\id_pilab" /inheritance:r /grant:r "SYSTEM:F" "BUILTIN\Administrators:F" | Out-Null
```

ไม่ต้อง restart service — relay retry ทุก `--refresh` (default 1s) เองอยู่แล้ว
พอ key พร้อมใช้ mirror จะ recover ทันทีในรอบถัดไป (`relay.log` ขึ้น
`mirror recovered`) เช็คง่ายๆ ด้วย `Get-Content <log-file> -Tail 20 -Wait`

โปรเจกต์ยังไม่เปิด HTTP collector โดยตั้งใจ เพราะการเปิด network endpoint เพิ่มภาระเรื่อง
authentication/TLS โดยไม่จำเป็นสำหรับ MVP; SSH transport ปลอดภัยและดูแลง่ายกว่า

## พัฒนาและทดสอบ

โค้ด CLI ใช้ `newCommandFlagSet` และ `parseCommandFlags` เป็นทางเข้ากลางสำหรับ
help/parse error ของทุก subcommand เพื่อให้ข้อความ help และ exit code (`0` สำหรับ
help, `2` สำหรับ flag ที่ไม่ถูกต้อง) สม่ำเสมอเมื่อเพิ่มคำสั่งใหม่ ส่วน renderer ของ
จอ framebuffer จะ reuse แหล่งสีหนึ่งครั้งต่อ rounded shape แทนการสร้างใหม่ทุก
scanline เพื่อลด allocation ใน render loop ~15 FPS โดยไม่เปลี่ยนภาพที่ได้

```bash
go test ./...
go vet ./...
go build -o bin/llm-status ./cmd/llm-status
```

Cross-build สำหรับ Pi 4:

```bash
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 \
  go build -trimpath -o bin/llm-status-linux-arm64 ./cmd/llm-status
```

สร้าง tarballs สำหรับ Linux ARM64/AMD64 พร้อม checksum:

```bash
bash scripts/package.sh v0.1.0
```

ถอนการติดตั้ง binary (ไม่ลบ state):

```bash
bash scripts/uninstall.sh
```

ใช้ `bash scripts/uninstall.sh --purge` เมื่อต้องการลบ state ด้วย

## แหล่งข้อมูลหลัก

- [Claude Code: Customize your status line](https://code.claude.com/docs/en/statusline)
- [Codex configuration reference](https://developers.openai.com/codex/config-reference)
- [Codex CLI source: external notify configuration](https://github.com/openai/codex/blob/main/codex-rs/core/src/config/mod.rs)
- [Claude Code: System requirements](https://code.claude.com/docs/en/setup)
- [Bubble Tea](https://github.com/charmbracelet/bubbletea)
- [Raspberry Pi OS documentation](https://www.raspberrypi.com/documentation/computers/os.html)
