"""Edit captured AFM screens into a captioned motion demo (Pillow, imageio-ffmpeg)."""

import argparse
import math
from pathlib import Path
import subprocess
import shutil
import tempfile

import imageio_ffmpeg
from PIL import Image, ImageDraw, ImageFont, ImageOps

WIDTH, HEIGHT, FPS = 1280, 900, 20
BG, PANEL, WHITE, MUTED = '#080f19', '#111d2d', '#edf2fa', '#8899b3'
PURPLE, GREEN, GOLD = '#9695ff', '#57d6a5', '#f0c36c'
SCENES = [
    ('intro', 6, 'Your agents. Your rules.', 'A real AFM playground. Zero model calls.'),
    ('scope-selected', 4, '01  Give the work a direction.', 'Answer in the dashboard. The run remembers.'),
    ('parallel', 4, '02  Five reviewers. One flow.', 'Parallel work. Separate histories. A shared result.'),
    ('cost', 5, '03  Follow the money.', 'Time, tokens and estimated cost for every stage.'),
    ('breakdown', 4, '04  Who spent what?', 'Switch from stages to individual agents.'),
    ('charts', 4, '05  Put a picture on it.', 'Compare estimated cost. Pick a bar. Inspect the jobs.'),
    ('timeline', 5, '06  Watch the work overlap.', 'Agent time adds up across parallel jobs.'),
    ('jobs', 4, '07  Down to one invocation.', 'The timeline takes you straight to the selected job.'),
    ('budget', 5, '08  The budget has entered the chat.', 'Active work finishes. The next stage waits.'),
    ('budget-raised', 5, '09  You hold the spending dial.', 'Raise the run limit. Save and continue.'),
    ('restart', 5, '10  Take five. Come right back.', 'Recorded progress survives the restart.'),
    ('resumed', 3, '11  Pick up where you left off.', 'Completed stages stay completed. The report resumes.'),
    ('report', 3, '12  The report has pictures.', 'An image in the feed. An artifact on disk.'),
    ('report-artifact', 3, '13  A result you can open.', 'Illustrative report; measured job times stay in Cost.'),
    ('outro', 5, 'Your agents. Your rules.', 'Try the playground. Then bring a real task.'),
]


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--captures', type=Path, required=True)
    parser.add_argument('--out', type=Path, required=True)
    parser.add_argument('--font', required=True)
    parser.add_argument('--bold-font', required=True)
    args = parser.parse_args()
    args.out.mkdir(parents=True, exist_ok=True)
    fonts = {}

    def font(size, bold=False):
        key = size, bold
        if key not in fonts:
            fonts[key] = ImageFont.truetype(args.bold_font if bold else args.font, size)
        return fonts[key]

    def text(draw, xy, value, size=22, color=WHITE, bold=False):
        draw.text(xy, value, font=font(size, bold), fill=color)

    def logo(draw, x=52, y=34):
        points = [(x + 12 * math.cos(math.pi / 3 * k - math.pi / 6),
                   y + 12 * math.sin(math.pi / 3 * k - math.pi / 6)) for k in range(6)]
        draw.polygon(points, outline=PURPLE, width=2)
        draw.ellipse((x-3, y-3, x+3, y+3), fill=PURPLE)
        text(draw, (x+23, y-15), 'afm', 27, bold=True)

    images = {}
    for name, *_ in SCENES:
        path = args.captures / f'{name}.png'
        if path.exists():
            images[name] = Image.open(path).convert('RGB')
    total = sum(scene[1] for scene in SCENES)
    ffmpeg = imageio_ffmpeg.get_ffmpeg_exe()
    with tempfile.TemporaryDirectory(prefix='afm-video-render-') as temp_dir:
        output = Path(temp_dir) / 'intermediate.mp4'
        process = subprocess.Popen([ffmpeg, '-hide_banner', '-loglevel', 'error', '-y',
            '-f', 'rawvideo', '-vcodec', 'rawvideo', '-pix_fmt', 'rgb24', '-s', f'{WIDTH}x{HEIGHT}',
            '-r', str(FPS), '-i', '-', '-an', '-c:v', 'libx264', '-preset', 'fast', '-crf', '20',
            '-pix_fmt', 'yuv420p', '-movflags', '+faststart', str(output)], stdin=subprocess.PIPE)
        elapsed = 0
        for index, (name, duration, title, subtitle) in enumerate(SCENES):
            print(f'render {name}', flush=True)
            for frame in range(duration * FPS):
                t = frame / FPS
                p = min(1, t / max(.01, duration - .3))
                ease = 1 - (1 - min(1, t / .5)) ** 3
                image = Image.new('RGB', (WIDTH, HEIGHT), BG)
                draw = ImageDraw.Draw(image)
                if name in ('intro', 'outro', 'restart'):
                    logo(draw)
                    text(draw, (820, 26), 'REAL AFM  /  SCRIPTED AGENTS', 16, MUTED)
                    if name == 'intro':
                        text(draw, (64, 126 + int(18 * (1-ease))), 'Your agents.', 78, bold=True)
                        text(draw, (64, 220 + int(18 * (1-ease))), 'Your rules.', 78, PURPLE, True)
                        text(draw, (68, 332), 'Long-running work. Human control.', 28, MUTED)
                        draw.rounded_rectangle((64, 440, 1216, 670), radius=18, fill=PANEL, outline='#26364d', width=2)
                        for i, color in enumerate(('#e78595', GOLD, GREEN)):
                            draw.ellipse((88+i*20,463,96+i*20,471), fill=color)
                        text(draw, (170, 456), 'PLAYGROUND / FROM A REPOSITORY CHECKOUT', 16, MUTED)
                        command = '$ python3 examples/playground/run.py --scenario review'
                        visible = command[:max(0, int((t-.7)*24))]
                        text(draw, (88, 520), visible + ('_' if frame % 16 < 10 else ''), 27, GREEN)
                        if t > 3.4:
                            text(draw, (88, 585), 'Dashboard ready. Your move.', 26)
                        text(draw, (68, 755), 'DIRECT', 19, PURPLE, True)
                        text(draw, (360, 755), 'INSPECT', 19, MUTED)
                        text(draw, (656, 755), 'CONTROL', 19, MUTED)
                        text(draw, (990, 755), 'FINISH', 19, MUTED)
                    elif name == 'restart':
                        text(draw, (64, 142), 'Take five.', 80, bold=True)
                        text(draw, (64, 238), 'Come right back.', 80, PURPLE, True)
                        draw.rounded_rectangle((64, 427, 1216, 704), radius=18, fill=PANEL, outline='#26364d', width=2)
                        text(draw, (90, 456), '$ ^C', 30, GOLD)
                        command = '$ python3 examples/playground/run.py --scenario review'
                        text(draw, (90, 522), command[:int(max(0,t-.6)*30)], 26, GREEN)
                        if t > 2:
                            text(draw, (117, 568), '--dir /path/to/the/same/playground', 25, GREEN)
                        if t > 3:
                            text(draw, (90, 630), '6 completed stages restored. Report continuing...', 25)
                        text(draw, (66, 759), 'Actual stop and restart captured in this demo.', 23, MUTED)
                    else:
                        text(draw, (64, 130), 'Your agents.', 76, bold=True)
                        text(draw, (64, 222), 'Your rules.', 76, PURPLE, True)
                        for i, line in enumerate(('Custom or ready-made pipelines.', 'Visible time and spend.', 'Budgets you can steer.', 'Progress you can resume.')):
                            if t > .25 + i*.24:
                                y = 365 + i*55
                                draw.line((78, y+17, 85, y+24, 98, y+8), fill=GREEN, width=3)
                                text(draw, (112, y), line, 29, GREEN if i==3 else WHITE)
                        draw.rounded_rectangle((64, 646, 1216, 778), radius=18, fill=PANEL, outline='#26364d', width=2)
                        text(draw, (88, 670), 'Try it: python3 examples/playground/run.py', 28, GREEN)
                        text(draw, (88, 724), 'github.com/akopichin/afm', 23, MUTED)
                else:
                    text(draw, (42, 25 + int(12*(1-ease))), title, 32, bold=True)
                    text(draw, (42, 66), subtitle, 19, GOLD if 'budget' in name else MUTED)
                    source = images[name]
                    if name == 'report-artifact':
                        plate = ImageOps.contain(source, (1180, 735), Image.Resampling.LANCZOS)
                    else:
                        # Crop browser chrome/header while retaining the actual captured product content.
                        source = source.crop((0, 100, source.width, source.height))
                        zoom = 1 + .035*p
                        cw, ch = int(source.width/zoom), int(source.height/zoom)
                        cx = int((source.width-cw)*(.35+.3*p))
                        cy = int((source.height-ch)*.45)
                        plate = ImageOps.contain(source.crop((cx,cy,cx+cw,cy+ch)), (1200, 735), Image.Resampling.LANCZOS)
                    image.paste(plate, ((WIDTH-plate.width)//2, 110 + (735-plate.height)//2))
                    draw = ImageDraw.Draw(image)
                    draw.rounded_rectangle((39, 108, 1241, 847), radius=10, outline='#26364d', width=2)
                    if name in ('scope-selected', 'charts', 'budget-raised'):
                        label = {'scope-selected':'YOU SET THE DIRECTION', 'charts':'CLICK A BAR > SEE ITS JOBS',
                                 'budget-raised':'20k TOKENS > SAVE AND CONTINUE'}[name]
                        y = 780 + int(25*(1-ease))
                        draw.rounded_rectangle((730,y,1210,y+42), radius=10, fill='#252b4c')
                        text(draw,(750,y+10),label,17,PURPLE,True)
                draw = ImageDraw.Draw(image)
                text(draw, (42, 866), 'AFM 3.1.4  /  EDITED CAPTURE  /  SIMULATED OUTPUT & PRICES', 13, MUTED)
                text(draw, (1105, 866), f'{index+1:02d} / {len(SCENES):02d}', 14, MUTED)
                draw.rectangle((0, HEIGHT-4, int(WIDTH*(elapsed+t)/total), HEIGHT), fill=PURPLE)
                if frame < 5 and index > 0:
                    # A brief slide-in cut keeps the pace without blank frames or flashes.
                    shift = int((1-ease)*38)
                    shifted = Image.new('RGB',image.size,BG)
                    shifted.paste(image,(shift,0))
                    image=shifted
                process.stdin.write(image.tobytes())
            elapsed += duration
        process.stdin.close()
        if process.wait() != 0:
            raise RuntimeError('Video encoder failed')
        subprocess.run([ffmpeg,'-hide_banner','-loglevel','error','-y','-i',str(output),
            '-filter_complex',
            '[0:v]select=lt(t\\,6)+between(t\\,18\\,22)+between(t\\,41\\,46)+between(t\\,55\\,61),setpts=N/FRAME_RATE/TB,fps=12,scale=960:-1:flags=lanczos,split[a][b];[a]palettegen=max_colors=160[p];[b][p]paletteuse=dither=bayer:bayer_scale=3',
            '-loop','0',str(args.out/'afm-playground.gif')],check=True)
        optimizer = shutil.which('gifsicle')
        if optimizer:
            optimized = Path(temp_dir) / 'optimized.gif'
            subprocess.run([optimizer, '-O3', str(args.out / 'afm-playground.gif'), '-o', str(optimized)], check=True)
            if optimized.stat().st_size < (args.out / 'afm-playground.gif').stat().st_size:
                shutil.copyfile(optimized, args.out / 'afm-playground.gif')
    print(f'Created {args.out / "afm-playground.gif"}', flush=True)



if __name__ == '__main__':
    main()
