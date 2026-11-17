# Cadence Forge · 音视频成片服务

Go HTTP 后端，以真实音视频字节为输入，提供版本化剪辑、字幕时间映射、MP4/WAV加工和可核验交付。没有Web前端。

## 运行

需要 Go 1.24+、Python 3（仅运行有界工作流检查）及带 libx264、AAC、loudnorm、tile、showwavespic 的 FFmpeg/ffprobe。当前本机实际验证 Go 1.27、FFmpeg/ffprobe 9.0.2；Docker构建定义采用Go 1.24和Debian媒体包，镜像运行尚需目标环境独立验证。

```sh
go build -trimpath -o bin/forged ./cmd/forged
./bin/forged
```

默认监听 `127.0.0.1:8734`，数据根 `var/cadence-forge`。可以设置 `FORGE_ADDR`、`FORGE_ROOT`、`FORGE_WORKERS`（1..8，默认2）、`FFMPEG_BIN`、`FFPROBE_BIN`。同一数据根仅允许一个服务进程。服务作为受信任环境中的媒体后端运行，面向公网接入前应部署认证和网络边界。

使用现有媒体工具运行所有真实工作流：

```sh
python3 scripts/workflow_check.py --workflow all   # 18 条基础流程
python3 scripts/sync_check.py                       # 双机位同步端到端
```

`workflow_check.py` 在临时数据根内生成4秒校准音画，不依赖外部素材或在线服务，完成18条工作流，包括真实强制终止及恢复。`--workflow`可选择运行清单中的某条工作流；所需的前置接收、版本编辑和渲染也会执行。`sync_check.py` 生成主控与被校准两段各12秒、带250ms可测脉冲的素材，第二机位时钟快800ppm，走完整 HTTP 与 FFmpeg 路径，测量成片脉冲与冻结计划。两者都是有界生产工作流核验，不声明为测试命令。

## 调用顺序

1. `POST /forge/v1/sources?name=校准片`：请求体为媒体原始字节，返回素材ID、SHA-256与流事实。
2. `POST /forge/v1/recipes`：例如 `{"name":"审片","format":"mp4","width":640,"height":360,"fps":24,"sample_rate":48000,"normalize":false,"target_lufs":-16}`。
3. `POST /forge/v1/sheets`：`{"name":"剪辑","spans":[{"source_id":"素材ID","in_ms":0,"out_ms":1500,"gain_db":0}],"cues":[{"source_id":"素材ID","start_ms":500,"end_ms":1000,"text":"校准"}]}`。
4. `GET /forge/v1/sheets/{id}/preview?recipe_id={id}`：预览当前最新修订的累计区间及WebVTT。片段按半开区间求交；同源重复出现分别映射。
5. `POST /forge/v1/renders`：提供 `Idempotency-Key`，正文 `{"sheet_id":"剪辑ID","sheet_revision":1,"recipe_id":"配方ID","recipe_revision":1}`。必须显式选择修订，提交冻结引用与时间计划；预览最新版本不替代提交选择。
6. `GET /forge/v1/renders/{id}`：查看排队、运行、成功、失败或取消及尝试历史。
7. `GET /forge/v1/renders/{id}/artifact`：下载检验通过的媒体；`?captions=1`下载字幕。`POST .../verify`重新核验；`GET .../bundle`返回媒体、字幕及冻结说明组成的ZIP。

## 双机位同步剪辑

两个现场机位的时钟会逐渐偏离。先登记两个素材，再建立一份不可变校准修订：

```json
POST /forge/v1/calibrations
{"name":"现场双机","master_source_id":"主控ID","camera_source_id":"副机ID",
 "anchors":[{"master_ms":0,"camera_ms":0},{"master_ms":6000,"camera_ms":6005},
            {"master_ms":11800,"camera_ms":11810}],
 "tolerance_ms":20}
```

主控源时钟即节目时间，锚点是同一事件在两段素材上的已确认读数；至少两个、严格推进且在两段素材时长内，两源都要有声。用 `PUT /calibrations/{id}` 与 `If-Match: 1` 追加修订，`GET /calibrations/{id}` 返回最新修订。

同步剪辑单改用逐段选机位与连续声音编排，不写 `spans`：

```json
POST /forge/v1/sheets
{"name":"双机位成片","sync_id":"校准ID",
 "shots":[{"camera_id":"主控ID","program_in_ms":2000,"program_out_ms":5000},
          {"camera_id":"副机ID","program_in_ms":5000,"program_out_ms":9000}],
 "sound":[{"camera_id":"主控ID","program_in_ms":2000,"program_out_ms":9000,"gain_db":0}],
 "cues":[{"source_id":"主控ID","start_ms":6000,"end_ms":7000,"text":"现场对白"}]}
```

画面 `shots` 与声音 `sound` 分别可编辑，但计划只构建一份全局对应关系，二者共享。声音必须连续覆盖整个节目；画面逐段平铺节目、无空隙。提交时显式冻结校准：

```json
POST /forge/v1/renders
{"sheet_id":"剪辑ID","sheet_revision":1,"recipe_id":"配方ID","recipe_revision":1,
 "calibration_id":"校准ID","calibration_revision":2}
```

`GET /sheets/{id}/preview?recipe_id=…&calibration_id=…&calibration_revision=…` 同参数预览。

### 取舍与不可完全满足的区间

采用**自然速度 + 锚点精确**：所有源区间按录制速度使用，不做 atempo/rubberband，也不做交付采样率之外的重采样；节目首尾与每个已确认锚点精确对应。两个精确点之间时钟漂移差为 Δ 时，在该区间中点做一次源时钟跳接，跳过或重复 |Δ| 毫秒素材，使该区间最大残差为 |Δ|/2。

仅按起点平移会让首端对齐、尾端偏差累积到整段漂移；整体变速能消除两端误差，却改变了对白与动作速度——本服务两者都不采用，也不宣称完成。`plan.sync` 给出逐锚点 `exact`/`residual_ms`、每个 gap 的 `drift_ms`、`splice_at_master_ms`、`splice_magnitude_ms`、`max_residual_ms`，以及 `inexact_intervals`（画面与声音各一组）。这些是交付说明的一部分，随成片一起冻结在 `composition.json`。连续声音每段只用一个自然速度偏移，不随画面跳接切开；它跨跳接时的残差单独列在 `sound_inexact_intervals`。

`sync_check.py` 会实测成片中的脉冲时刻，用 24fps 一帧与 60ms 音频窗口判定，并把实测误差与计划残差并排报告；它不声称任意漂移率或任意素材都能达到该误差。若节目越出已确认锚点，或声画编排不成连续覆盖，提交会被拒绝，而不是外推。

## 编辑与恢复

`PUT /sheets/{id}`、`PUT /recipes/{id}`及`PUT /calibrations/{id}`使用十进制修订号 `If-Match: 1`。本服务的该头为应用级修订约定，并非通用HTTP条件请求实现。保存只追加历史，过期写入409。输入中的服务管理ID与修订由服务生成。

双机位渲染在排队时冻结校准修订、剪辑修订、配方修订、源摘要与时间计划。之后再保存校准修订只影响后续提交；已排队、运行中、失败后重试或重启恢复的任务继续使用原冻结对应关系，因此字幕、成片与交付说明始终解释同一份修订。线性剪辑单不能引用校准，同步剪辑单必须显式给出校准。

`POST /renders/{id}/cancel`撤回排队或运行中的交付；成功确认后该任务不能新提交成片。进程停止是后续动作，并不承诺确认瞬间资源已全部释放。成功成片不可再取消。`POST /renders/{id}/retry`仅接受failed，最多三次尝试，沿用原冻结输入。重启时running变为failed/interrupted，等待显式重试，queued继续排队。

`GET /events?after=0`返回至多200条严格后继事件。序号是持久顺序，时间戳仅用于解释。`GET /state`返回当前一致快照，适合受信任客户端。

## 分析入口

`POST /sources/{id}/loudness`测量源LUFS、真峰值和动态范围，静音或不可测结果使用null并指示silent。`GET /sources/{id}/visual?mode=storyboard`生成四格图；`mode=waveform`生成640×120单声道概览。它们是有成本的同步分析，请求受独立超时约束；波形合并声道可能隐藏反相内容，故事板不等于逐帧审片。

## 支持范围与限制

单素材最多64 MiB、约120秒、3840×2160和8声道。剪辑最多32段、120秒、500条字幕，段长至少100ms；输出MP4/H.264/AAC或WAV/16位PCM，采样率44.1或48kHz。MP4要求每段有视频；无声视频生成对应静音。WAV要求每段有声音。视频保持完整画面并留边，统一像素比例与帧率，声音统一立体声。归一化为单遍响度滤镜，不保证任何短片都精确达到目标值。

同步剪辑单最多64个机位段与64个声音段，节目不超过120秒、每段至少100ms；校准最多64个锚点、容差1..500ms。节目必须完全落在已确认锚点范围内，首尾也按确认对应关系插值定位，超出即拒绝而非外推。画面在跳接点拆分并保持自然速度；声音不拆分。跳接会造成一次最多 |Δ| 毫秒的素材跳过或重复，若素材在跳接点附近恰有对白，该处会有可察觉的接缝——计划会把位置与幅度报出来，但不会自动避免。

时长基准为毫秒，编码后的帧与采样可能有容差。检验检查流、尺寸、采样率、时长与摘要，不代表内容正确、逐帧同步或主观质量已通过人工审片。同步成片额外检查画面与声音都在场，但不做逐帧或逐采样对齐的自动断言；那需要外部测量，参见 `sync_check.py`。禁止容器中的远程协议与非白名单封装，但媒体进程仍需要部署侧隔离。

接收先耐久保留字节再探测登记，失败可能留下未引用对象。成功产物先检验、耐久提升，再提交可见事实；崩溃可能留下未登记产物。当前不自动回收这些内容。`DELETE /sources/{id}`只退休无历史引用的素材记录，保守保留对象；历史剪辑、字幕、校准和渲染血缘均阻止退休。

状态写入使用同文件系统临时文件、fsync、替换与父目录同步；目录同步失败时服务拒绝后续写入并要求恢复核对。对象及产物与状态分阶段提交，已生成目录不能自行视为交付成功。请备份整个数据根，并在停止唯一服务实例后进行一致复制。

## 容器

```sh
docker build -t cadence-forge .
docker run --rm -p 8734:8734 -v cadence-forge-data:/data cadence-forge
```

运行身份为10001。这里的容器定义不是跨平台运行通过声明；实际验证范围与本机工作流结果见交付记录。
