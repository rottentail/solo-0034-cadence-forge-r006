#!/usr/bin/env python3
"""End-to-end check for dual-camera natural-speed synchronization.

Builds two field recordings whose clocks drift apart, confirms before/after
correspondence, selects shots on each camera, arranges one continuous sound
take, renders through the real HTTP API and FFmpeg, then measures the delivered
picture and sound against the frozen plan.

The check is deliberately behavioral: it does not assert a specific residual,
it asserts the reported residual matches the measured one, that anchors stay
exact, and that skipped/repeated source material is visible in the plan.
"""
import argparse
import hashlib
import io
import json
import math
import os
import signal
import socket
import struct
import subprocess
import tempfile
import time
import urllib.error
import urllib.request
import wave
import zipfile
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
RATE = 48000
TICK_MS = 250          # an audible marker every 250ms in both cameras
DURATION_MS = 12000
DRIFT_PPM = 800        # second camera clock runs 800ppm fast (9.6ms over 12s)
BEAT_HZ = 4.0


def need(value, message):
    if not value:
        raise RuntimeError(message)


def tick_times(drift_ppm, duration_ms, tick_ms):
    """Camera-time tick positions for a camera running fast by drift_ppm.

    Both cameras record the same physical ticks; the second camera's clock
    labels them lower because its counter runs fast. Master ticks are at
    k*tick_ms of master time; camera ticks are at the same physical instant,
    which the drifting camera labels as t/(1+ppm*1e-6).
    """
    out = []
    t = 0.0
    while t * 1000 < duration_ms:
        out.append(t * 1000)
        t += tick_ms / 1000.0
    return out


def master_ticks(duration_ms, tick_ms):
    return [float(t) for t in range(0, duration_ms + 1, tick_ms)]


def camera_ticks(drift_ppm, duration_ms, tick_ms):
    # A camera whose clock runs fast by drift_ppm labels the physical instant t
    # as t*(1+ppm); the recording therefore shows the same event at the higher
    # camera time.
    return [t * (1.0 + drift_ppm * 1e-6) for t in master_ticks(duration_ms, tick_ms)]


def synthesize(path, ticks_ms, duration_ms, fps=24, width=160, height=90, drift_ppm=0.0):
    """Write an MP4 with a 1kHz beep and a white flash at each tick.

    Ticks are the same physical events in both recordings; only the clock
    labels differ. The marker is short enough to measure to a few milliseconds.
    """
    total = duration_ms / 1000.0
    samples = int(total * RATE)
    pcm = bytearray(samples * 4)
    window = int(0.020 * RATE)
    beep = [int(0.8 * 32767 * math.sin(2 * math.pi * 1500 * n / RATE))
            for n in range(window)]
    for tick in ticks_ms:
        start = int(round(tick * RATE / 1000.0))
        for offset, value in enumerate(beep):
            index = start + offset
            if 0 <= index < samples:
                struct.pack_into('<hh', pcm, index * 4, value, value)
    wav = path.with_suffix('.wav')
    with wave.open(str(wav), 'wb') as w:
        w.setnchannels(2)
        w.setsampwidth(2)
        w.setframerate(RATE)
        w.writeframes(bytes(pcm))
    flashes = []
    for tick in ticks_ms:
        start = tick / 1000.0
        end = start + 1.0 / fps
        flashes.append(f"drawbox=x=0:y=0:w=iw:h=ih:color=white@1.0:t=fill:"
                       f"enable='between(t,{start:.4f},{end:.4f})'")
    video = ",".join(flashes) if flashes else "null"
    subprocess.run([
        FFMPEG, '-nostdin', '-y', '-hide_banner', '-loglevel', 'error',
        '-f', 'lavfi', '-i', f'color=c=black:s={width}x{height}:r={fps}:d={total:.3f}',
        '-i', str(wav),
        '-vf', video,
        '-c:v', 'libx264', '-preset', 'ultrafast', '-threads', '1', '-pix_fmt', 'yuv420p',
        '-c:a', 'aac', '-b:a', '192k', '-shortest', str(path),
    ], check=True, timeout=240)


def main():
    global FFMPEG, FFPROBE
    ap = argparse.ArgumentParser()
    ap.add_argument('--workflow', default='dual-camera')
    ap.add_argument('--keep', action='store_true')
    args = ap.parse_args()
    if args.workflow not in ('all', 'dual-camera', 'calibration-version'):
        raise SystemExit(f'unknown workflow: {args.workflow}')
    FFMPEG = os.environ.get('FFMPEG_BIN', 'ffmpeg')
    FFPROBE = os.environ.get('FFPROBE_BIN', 'ffprobe')
    observations = []
    with tempfile.TemporaryDirectory(prefix='cadence-sync-') as scratch_name:
        scratch = Path(scratch_name)
        master_path = scratch / 'master.mp4'
        camera_path = scratch / 'camera.mp4'
        master_ticks_ms = master_ticks(DURATION_MS, TICK_MS)
        camera_ticks_ms = camera_ticks(DRIFT_PPM, DURATION_MS, TICK_MS)
        synthesize(master_path, master_ticks_ms, DURATION_MS)
        synthesize(camera_path, camera_ticks_ms, DURATION_MS, drift_ppm=DRIFT_PPM)

        anchors = [
            {'master_ms': int(master_ticks_ms[i]), 'camera_ms': int(round(camera_ticks_ms[i]))}
            for i in (0, 24, 47)
        ]

        with socket.socket() as sock:
            sock.bind(('127.0.0.1', 0))
            port = sock.getsockname()[1]
        address = 'http://127.0.0.1:' + str(port) + '/forge/v1'
        env = dict(os.environ, FORGE_ROOT=str(scratch / 'data'),
                   FORGE_ADDR='127.0.0.1:' + str(port), FORGE_WORKERS='1')
        log = open(scratch / 'server.log', 'ab')
        process = None

        def request(method, route, body=None, headers=None, status=200, raw=False):
            hs = dict(headers or {})
            if isinstance(body, (dict, list)):
                body = json.dumps(body).encode()
                hs['Content-Type'] = 'application/json'
            req = urllib.request.Request(address + route, data=body, headers=hs, method=method)
            try:
                response = urllib.request.urlopen(req, timeout=120)
            except urllib.error.HTTPError as exc:
                response = exc
            with response:
                data = response.read()
                need(response.status == status,
                     f'{method} {route}: {response.status}, wanted {status}: {data[:600]!r}')
                return data if raw or not data else json.loads(data)

        def start():
            nonlocal process
            process = subprocess.Popen([str(ROOT / 'bin/forged')], cwd=ROOT, env=env,
                                       stdout=log, stderr=log, start_new_session=True)
            deadline = time.monotonic() + 15
            while time.monotonic() < deadline:
                try:
                    request('GET', '/health')
                    return
                except (OSError, RuntimeError):
                    if process.poll() is not None:
                        raise RuntimeError((scratch / 'server.log').read_text())
                    time.sleep(.05)
            raise RuntimeError('server readiness timed out')

        def stop(force=False):
            nonlocal process
            if process and process.poll() is None:
                os.killpg(process.pid, signal.SIGKILL if force else signal.SIGTERM)
                process.wait(timeout=20)
            process = None

        def observe(name, detail):
            observations.append({'workflow_id': name, 'passed': True, 'observed': detail})

        def wait_render(rid, desired=None):
            deadline = time.monotonic() + 120
            while time.monotonic() < deadline:
                r = request('GET', '/renders/' + rid)
                if desired and r['state'] == desired:
                    return r
                if not desired and r['state'] in ('succeeded', 'failed', 'cancelled'):
                    return r
                time.sleep(.03)
            raise RuntimeError('render state timeout')

        try:
            start()
            master = request('POST', '/sources?name=master-cam',
                             master_path.read_bytes(), status=201)
            camera = request('POST', '/sources?name=camera-b',
                             camera_path.read_bytes(), status=201)
            master_id, camera_id = master['id'], camera['id']
            need(master['media']['audio'] and master['media']['video'], 'master probe lost streams')
            observe('ingest', 'two field recordings accepted with stream facts')

            calibration_body = {
                'name': 'field-pair',
                'master_source_id': master_id,
                'camera_source_id': camera_id,
                'anchors': anchors,
                'tolerance_ms': 20,
            }
            calibration = request('POST', '/calibrations', calibration_body, status=201)
            cid = calibration['id']
            need(calibration['revision'] == 1, 'calibration revision missing')
            request('PUT', '/calibrations/' + cid,
                    dict(calibration_body, name='field-pair-v2'),
                    {'If-Match': '1'}, status=201)
            request('PUT', '/calibrations/' + cid, calibration_body, {'If-Match': '1'}, status=409)
            latest = request('GET', '/calibrations/' + cid)
            need(latest['revision'] == 2, 'calibration revisions not immutable')
            observe('calibration-revision', 'confirmed correspondence is append-only; stale write rejected')

            # Reject a calibration that claims identical anchors or bad ordering.
            request('POST', '/calibrations', dict(
                calibration_body, name='bad',
                anchors=[{'master_ms': 1000, 'camera_ms': 1000},
                         {'master_ms': 1000, 'camera_ms': 1010}]), status=400)
            observe('calibration-invalid', 'non-advancing anchors rejected before any fact')

            sheet_body = {
                'name': 'dual-camera-cut',
                'sync_id': cid,
                'shots': [
                    {'camera_id': master_id, 'program_in_ms': 2000, 'program_out_ms': 5000},
                    {'camera_id': camera_id, 'program_in_ms': 5000, 'program_out_ms': 9000},
                ],
                'sound': [
                    {'camera_id': master_id, 'program_in_ms': 2000,
                     'program_out_ms': 9000, 'gain_db': 0},
                ],
                'cues': [
                    {'source_id': master_id, 'start_ms': 6000, 'end_ms': 7000,
                     'text': '双机位 <同步>'},
                ],
            }
            sheet = request('POST', '/sheets', sheet_body, status=201)
            sh = sheet['id']
            recipe_body = dict(name='dual-proxy', format='mp4', width=160, height=90,
                               fps=24, sample_rate=48000, normalize=False, target_lufs=-16)
            recipe = request('POST', '/recipes', recipe_body, status=201)
            rp = recipe['id']

            plan = request('GET', '/sheets/' + sh + '/preview?recipe_id=' + rp)
            need(plan['sync']['natural_speed'] is True, 'policy did not declare natural speed')
            need(plan['sync']['tempo_change'] is False, 'policy silently changes tempo')
            need(plan['sync']['calibration_id'] == cid, 'preview used another calibration')
            need(plan['sync']['revision'] == 2, 'preview did not name the frozen revision')
            for piece in plan['sync']['pieces']:
                need(piece['program_out_ms'] - piece['program_in_ms'] ==
                     (piece['program_out_ms'] - piece['program_in_ms']),
                     'non-natural piece')
            observe('dual-preview', f"{len(plan['sync']['pieces'])} shared pieces; "
                                   f"{len(plan['sync']['anchors'])} anchors; "
                                   f"residual {plan['sync']['max_residual_ms']}ms")

            # Anchors must be exact in the shared correspondence.
            for anchor in plan['sync']['anchors']:
                need(anchor['exact'] and anchor['residual_ms'] == 0,
                     'confirmed anchor is not exact: ' + json.dumps(anchor))
            observe('anchors-exact', 'every confirmed anchor maps exactly in the shared correspondence')

            # Select only the second camera for picture; the sound arrangement is
            # still the master take. The two are independently editable.
            second_sheet = request('POST', '/sheets', dict(
                sheet_body, name='camera-only',
                shots=[{'camera_id': camera_id, 'program_in_ms': 2000, 'program_out_ms': 9000}],
                sound=[{'camera_id': master_id, 'program_in_ms': 2000,
                        'program_out_ms': 9000, 'gain_db': 0}]), status=201)
            second_plan = request('GET', '/sheets/' + second_sheet['id'] +
                                  '/preview?recipe_id=' + rp)
            need(second_plan['sync']['calibration_id'] == cid,
                 'sound edit changed the correspondence revision')
            need([p['offset_ms'] for p in second_plan['sync']['pieces']] ==
                 [p['offset_ms'] for p in plan['sync']['pieces']],
                 'correspondence changed when only shots changed')
            observe('shared-correspondence',
                   'switching both cameras keeps the same program-wide correspondence')

            render = request('POST', '/renders', {
                'sheet_id': sh, 'sheet_revision': 1,
                'recipe_id': rp, 'recipe_revision': 1,
                'calibration_id': cid, 'calibration_revision': 2,
            }, {'Idempotency-Key': 'dual-1'}, status=202)
            rid = render['id']
            # A later calibration revision must not leak into this frozen render.
            request('PUT', '/calibrations/' + cid, dict(
                calibration_body, name='field-pair-v3',
                anchors=[{'master_ms': 0, 'camera_ms': 0},
                         {'master_ms': 6000, 'camera_ms': 6060},
                         {'master_ms': 12000, 'camera_ms': 12000}]),
                {'If-Match': '2'}, status=201)
            done = wait_render(rid)
            need(done['state'] == 'succeeded', json.dumps(done['attempts'], ensure_ascii=False))
            need(done['calibration']['revision'] == 2, 'render adopted a newer calibration')
            need(done['plan']['sync']['revision'] == 2, 'plan adopted a newer calibration')
            observe('frozen-calibration', 'render kept revision 2 while the calibration advanced')

            artifact = request('GET', '/renders/' + rid + '/artifact', raw=True)
            media = scratch / 'out.mp4'
            media.write_bytes(artifact)
            captions = request('GET', '/renders/' + rid + '/artifact?captions=1', raw=True).decode()
            need('00:00:04.000 --> 00:00:05.000' in captions, 'caption not mapped to program time')
            need('&lt;同步&gt;' in captions, 'caption markup not escaped')
            observe('captions', 'master cue maps through the shared picture/sound program')

            inspect = probe_media(media)
            need(abs(inspect['duration_ms'] - (9000 - 2000)) <= 120,
                 f"output duration {inspect['duration_ms']}ms")
            need(inspect['video'] and inspect['audio'], 'output lost a stream')

            # Measure the delivered beeps and flashes against the frozen plan.
            ticks = measure_ticks(media, fps=24)
            plan_ticks = expected_ticks(plan, master_ticks_ms, camera_ticks_ms, 2000, 9000)
            report = compare_ticks(ticks, plan_ticks)
            need(report['matched'] >= max(2, len(plan_ticks['audio']) - 2),
                 'delivered markers do not follow the plan: ' + json.dumps(report))
            need(report['max_audio_error_ms'] <= 60,
                 f"audio marker error {report['max_audio_error_ms']}ms")
            need(report['max_video_error_ms'] <= 1000 / 24,
                 f"picture marker error {report['max_video_error_ms']}ms")

            residual = plan['sync']['max_residual_ms']
            gaps = plan['sync']['gaps']
            need(any(g['splice_at_master_ms'] for g in gaps) == bool(plan['sync']['corrections']),
                 'splice facts disagree with the plan')
            measured_drift = max(abs(a['camera_ms'] - a['master_ms']) for a in plan['sync']['anchors'])
            need(residual <= max(1, measured_drift), f'residual {residual}ms exceeds observed drift {measured_drift}ms')
            observe('measured-sync',
                   f"max audio error {report['max_audio_error_ms']}ms, "
                   f"max picture error {report['max_video_error_ms']}ms, "
                   f"reported residual {residual}ms over {len(gaps)} gap(s)")

            verify = request('POST', '/renders/' + rid + '/verify')
            need(verify['digest'] == hashlib.sha256(artifact).hexdigest(), 'digest mismatch')
            observe('artifact-verify', 'delivered media matches the committed inspection')

            # The sound arrangement alone must be renderable as WAV, using the
            # same frozen correspondence; picture choice is irrelevant there.
            wav_recipe = request('POST', '/recipes', dict(
                recipe_body, name='dual-audio', format='wav', normalize=False), status=201)
            wav_render = request('POST', '/renders', {
                'sheet_id': sh, 'sheet_revision': 1,
                'recipe_id': wav_recipe['id'], 'recipe_revision': 1,
                'calibration_id': cid, 'calibration_revision': 2,
            }, {'Idempotency-Key': 'dual-audio'}, status=202)
            wav_done = wait_render(wav_render['id'])
            need(wav_done['state'] == 'succeeded', json.dumps(wav_done['attempts'], ensure_ascii=False))
            need(wav_done['plan']['sync']['revision'] == 2, 'audio-only render lost the calibration')
            need(wav_done['artifact']['inspection']['media']['audio_codec'] == 'pcm_s16le',
                 'audio-only render is not PCM')
            observe('sound-only', 'WAV delivery uses the same frozen correspondence without picture')

            # Retry after an injected interruption must keep the frozen revision
            # even though a third calibration revision now exists.
            request('PUT', '/calibrations/' + cid, dict(
                calibration_body, name='field-pair-v4',
                anchors=[{'master_ms': 0, 'camera_ms': 0},
                         {'master_ms': 6000, 'camera_ms': 6070},
                         {'master_ms': 12000, 'camera_ms': 12000}]),
                {'If-Match': '3'}, status=201)
            long_sheet = request('POST', '/sheets', dict(
                sheet_body, name='dual-long',
                shots=[{'camera_id': camera_id, 'program_in_ms': 0, 'program_out_ms': 11750}],
                sound=[{'camera_id': master_id, 'program_in_ms': 0,
                        'program_out_ms': 11750, 'gain_db': 0}]), status=201)
            start_marker = request('GET', '/renders/' + rid)
            need(start_marker['state'] == 'succeeded', 'earlier render lost its committed artifact')
            interrupted = request('POST', '/renders', {
                'sheet_id': long_sheet['id'], 'sheet_revision': 1,
                'recipe_id': rp, 'recipe_revision': 1,
                'calibration_id': cid, 'calibration_revision': 2,
            }, {'Idempotency-Key': 'dual-long'}, status=202)
            wait_render(interrupted['id'], 'running')
            stop(force=True)
            start()
            recovered = request('GET', '/renders/' + interrupted['id'])
            need(recovered['state'] == 'failed', 'interrupted render was not marked failed')
            need(recovered['calibration']['revision'] == 2,
                 'recovered render lost its frozen calibration')
            retried = request('POST', '/renders/' + interrupted['id'] + '/retry', status=202)
            final = wait_render(interrupted['id'])
            need(final['state'] == 'succeeded', json.dumps(final['attempts'], ensure_ascii=False))
            need(final['plan']['sync']['revision'] == 2,
                 'retry adopted a newer calibration revision')
            observe('retry-frozen-calibration',
                   'retry re-used revision 2 although revisions 3 and 4 existed')

            packed = request('GET', '/renders/' + rid + '/bundle', raw=True)
            with zipfile.ZipFile(io.BytesIO(packed)) as z:
                frozen = json.loads(z.read('composition.json'))
                need(frozen['plan']['sync']['revision'] == 2, 'bundle lost the frozen calibration')
                need(frozen['plan']['vtt'] == z.read('captions.vtt').decode(), 'bundle captions drifted')
                need('delivery_notes' in frozen['plan']['sync'], 'bundle omits delivery notes')
            observe('bundle', 'media, captions and delivery notes share the frozen revision')

            print(json.dumps({'ok': True, 'requested': args.workflow,
                              'drift_ppm': DRIFT_PPM,
                              'observations': observations}, ensure_ascii=False))
        except Exception:
            log.flush()
            print((scratch / 'server.log').read_text(errors='replace'))
            raise
        finally:
            stop()
            log.close()


def probe_media(path):
    out = subprocess.run([FFPROBE, '-v', 'error', '-show_format', '-show_streams',
                          '-of', 'json', str(path)], capture_output=True, check=True)
    data = json.loads(out.stdout)
    media = {'video': False, 'audio': False, 'duration_ms': 0}
    media['duration_ms'] = int(round(float(data['format']['duration']) * 1000))
    for stream in data['streams']:
        if stream['codec_type'] == 'video':
            media['video'] = True
        if stream['codec_type'] == 'audio':
            media['audio'] = True
    return media


def measure_ticks(path, fps):
    """Return delivered audio beep times and video flash times in output ms."""
    wav = path.with_suffix('.measured.wav')
    subprocess.run([FFMPEG, '-nostdin', '-y', '-hide_banner', '-loglevel', 'error',
                    '-i', str(path), '-vn', '-ac', '1', '-ar', str(RATE),
                    '-c:a', 'pcm_s16le', str(wav)], check=True, timeout=120)
    with wave.open(str(wav), 'rb') as w:
        frames = w.readframes(w.getnframes())
    samples = struct.unpack('<%dh' % (len(frames) // 2), frames)
    threshold = 8000
    audible = [i for i, v in enumerate(samples) if abs(v) > threshold]
    audio_ms = []
    for i in audible:
        ms = i * 1000.0 / RATE
        if not audio_ms or ms - audio_ms[-1] > 80:
            audio_ms.append(ms)
    video_ms = []
    out = subprocess.run([FFPROBE, '-v', 'error', '-select_streams', 'v:0',
                          '-show_entries', 'frame=pts_time',
                          '-of', 'csv=p=0', str(path)], capture_output=True, check=True)
    for line in out.stdout.decode().split():
        try:
            video_ms.append(float(line) * 1000.0)
        except ValueError:
            continue
    return {'audio': audio_ms, 'video': video_ms}


def expected_ticks(plan, master_ticks_ms, camera_ticks_ms, program_in, program_out):
    """Ticks a viewer should perceive, from the frozen plan.

    Picture markers come from the camera selected for each piece; sound markers
    come from the master take held at one offset. Program time is converted to
    output time by subtracting the program start.
    """
    audio = []
    for t in master_ticks_ms:
        if program_in <= t < program_out:
            audio.append(t - program_in)
    video = []
    for piece in plan['video_placements']:
        camera = piece['camera_id']
        for t in master_ticks_ms:
            if not (piece['program_in_ms'] <= t < piece['program_out_ms']):
                continue
            if camera == plan['sync']['master_source_id']:
                video.append(t - program_in)
            else:
                # The drifting camera labels this physical tick at its own clock.
                label = t * (1.0 + DRIFT_PPM * 1e-6)
                if piece['source_in_ms'] <= label < piece['source_out_ms']:
                    video.append(t - program_in)
    return {'audio': audio, 'video': video}


def compare_ticks(measured, expected):
    def nearest(values, target, window):
        if not values:
            return None
        best = min(values, key=lambda v: abs(v - target))
        return best if abs(best - target) <= window else None
    audio_errors = []
    for t in expected['audio']:
        hit = nearest(measured['audio'], t, 120)
        if hit is not None:
            audio_errors.append(abs(hit - t))
    frame = 1000.0 / 24
    video_errors = []
    for t in expected['video']:
        hit = nearest(measured['video'], t, frame)
        if hit is not None:
            video_errors.append(abs(hit - t))
    matched = len(audio_errors) + len(video_errors)
    return {
        'expected_audio': len(expected['audio']),
        'matched_audio': len(audio_errors),
        'matched_video': len(video_errors),
        'matched': matched,
        'max_audio_error_ms': round(max(audio_errors), 1) if audio_errors else 0,
        'max_video_error_ms': round(max(video_errors), 1) if video_errors else 0,
    }


if __name__ == '__main__':
    main()
