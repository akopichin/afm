"""Capture the real playground UI; requires Playwright and an installed AFM."""

import argparse
import importlib.util
import json
import os
from pathlib import Path
import shutil
import signal
import subprocess
import time
import urllib.request

from playwright.sync_api import sync_playwright


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--out', type=Path, required=True)
    parser.add_argument('--browser', required=True)
    parser.add_argument('--afm', default='afm')
    parser.add_argument('--port', type=int, default=18981)
    args = parser.parse_args()
    out = args.out.resolve()
    out.mkdir(parents=True, exist_ok=True)
    root = out / 'run'
    if root.exists():
        parser.error('Use a fresh output directory for a new recording.')
    repo = Path(__file__).resolve().parents[2]
    spec = importlib.util.spec_from_file_location('playground', repo / 'examples/playground/run.py')
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    module.prepare(root, 'review')
    config = root / '.afm/config.yaml'
    config.write_text(config.read_text().replace('open_browser: true', 'open_browser: false'))
    binary = shutil.which(args.afm)
    if not binary:
        parser.error('AFM was not found.')
    base = f'http://127.0.0.1:{args.port}'
    env = dict(os.environ, AFM_DEMO_DELAY='2', AFM_SIDEAGENT='0', AFM_COLLABORATION='0',
               AFM_ACCOUNTING='1', AFM_ACCOUNTING_SHOW_MONEY='1')
    log = (out / 'runtime.log').open('w')

    def start():
        return subprocess.Popen([binary, '--dir', str(root), 'run', 'flow.yaml', '--port', str(args.port)],
                                cwd=root, stdout=log, stderr=log, env=env)

    def stop(process):
        if process.poll() is None:
            process.send_signal(signal.SIGINT)
            process.wait(timeout=20)

    def status():
        with urllib.request.urlopen(base + '/api/status', timeout=2) as response:
            return json.load(response)

    def until(predicate):
        deadline = time.monotonic() + 45
        while time.monotonic() < deadline:
            try:
                if predicate():
                    return
            except (OSError, ValueError):
                pass
            time.sleep(.1)
        raise RuntimeError('Timed out waiting for AFM')

    process = start()
    try:
        until(lambda: any(s['status'] == 'awaiting_user_input' for s in status()['stages']))
        with sync_playwright() as p:
            browser = p.chromium.launch(executable_path=args.browser, headless=True)
            page = browser.new_page(viewport={'width': 1440, 'height': 1000}, device_scale_factor=1)
            page.set_default_timeout(10000)
            page.goto(base)
            page.wait_for_timeout(500)
            actions = {}

            def shot(name, target=None):
                page.wait_for_timeout(250)
                if target is not None:
                    target.scroll_into_view_if_needed()
                    actions[name] = target.bounding_box()
                page.screenshot(path=str(out / f'{name}.png'))
                print(name, flush=True)

            shot('scope', page.get_by_role('button', name='A small feature', exact=True))
            page.get_by_role('button', name='A small feature', exact=True).click()
            shot('scope-selected', page.get_by_role('button', name='Send answer'))
            page.get_by_role('button', name='Send answer').click()
            until(lambda: sum(s['status'] == 'running' for s in status()['stages']) >= 3)
            page.wait_for_timeout(800)
            shot('parallel')
            until(lambda: sum(s['status'] == 'done' for s in status()['stages']) == 6)
            page.get_by_text('Cost', exact=True).click()
            shot('cost')
            page.get_by_text('Models & agents', exact=True).click()
            page.get_by_role('button', name='By agent', exact=True).click()
            shot('breakdown')
            page.get_by_role('button', name='Charts', exact=True).click()
            shot('charts', page.locator('.usage-bar-row').first)
            page.get_by_role('button', name='Agent time', exact=True).click()
            shot('time')
            page.locator('.usage-timeline').scroll_into_view_if_needed()
            shot('timeline', page.locator('.usage-timeline-job').last)
            page.locator('.usage-timeline-job').last.click()
            shot('jobs')
            page.get_by_role('button', name='Back to Cost overview').click()
            page.locator('[data-scope="run"] button').click()
            shot('budget')
            page.get_by_label('Custom limit', exact=True).check()
            page.get_by_role('textbox', name='Tokens', exact=True).fill('20k')
            shot('budget-raised', page.get_by_role('button', name='Save and continue', exact=True))
            page.get_by_role('button', name='Save and continue', exact=True).click()
            until(lambda: next(s for s in status()['stages'] if s['id'] == 'report')['status'] == 'running')
            stop(process)
            shot('stopped')
            process = start()
            until(lambda: len(status()['stages']) == 7)
            page.reload()
            page.wait_for_timeout(500)
            shot('resumed')
            until(lambda: all(s['status'] == 'done' for s in status()['stages']))
            page.get_by_text('Feed', exact=True).click()
            page.locator('[data-stage-id="report"] .stage-row').click()
            page.wait_for_timeout(500)
            print(page.locator('body').inner_text()[-1500:], flush=True)
            shot('report')
            shutil.copyfile(next(root.glob('.afm/runs/*/report/artifacts/report.png')), out / 'report-artifact.png')
            (out / 'actions.json').write_text(json.dumps(actions, indent=2))
            browser.close()
    finally:
        stop(process)
        log.close()


if __name__ == '__main__':
    main()
