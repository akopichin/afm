"""Regenerate bundled illustrative reports: python3 -m pip install Pillow first."""

from pathlib import Path
import argparse

from PIL import Image, ImageDraw, ImageFont


def render(scenario, workers, font_path):
    image = Image.new("RGB", (1400, 980), "#0c1320")
    draw = ImageDraw.Draw(image)

    def text(x, y, value, size=22, color="#dfe8f5"):
        draw.text((x, y), value, font=ImageFont.truetype(font_path, size), fill=color)

    def box(bounds, fill, radius=14):
        draw.rounded_rectangle(bounds, radius=radius, fill=fill)

    text(56, 40, "AFM / PLAYGROUND REPORT", 18, "#8aa8d5")
    text(56, 79, scenario.title() + " pipeline", 42)
    text(56, 140, "SIMULATED DATA  /  illustrative timeline and prices, not a provider bill", 20, "#e8bd6b")
    jobs = len(workers) + 2
    cards = [("AGENT JOBS", str(jobs)), ("SCRIPTED TOKENS", f"{jobs * 1440:,}"),
             ("ESTIMATED COST", f"${jobs * .0072:.4f}"), ("RUN BUDGET", "3k > 20k")]
    for i, (label, value) in enumerate(cards):
        x = 56 + i * 328
        box((x, 195, x + 308, 301), "#172438")
        text(x + 20, 211, label, 16, "#95a9c6")
        text(x + 20, 244, value, 33)
    text(56, 340, "When the simulated agents work", 28)
    text(56, 385, "Schematic only. Actual job duration and spend are in Cost > Models & agents.", 19, "#95a9c6")
    left, scale = 300, 12
    rows = [("Scope", 0, 5, "#91a5ff")]
    rows += [(worker.replace("-", " ").title(), 5, 30, "#59d0a6") for worker in workers]
    rows += [("Report", 45, 15, "#e8bd6b")]
    for tick in (0, 15, 30, 45, 60):
        x = left + tick * scale
        draw.line((x, 458, x, 458 + len(rows) * 45), fill="#23334b", width=1)
        text(x, 430, f"{tick}s", 16, "#95a9c6")
    text(1125, 430, "EST. / JOB", 16, "#95a9c6")
    for index, (label, start, duration, color) in enumerate(rows):
        y = 465 + index * 45
        text(56, y + 1, label, 20)
        box((left + start * scale, y, left + (start + duration) * scale, y + 28), color, 5)
        text(1125, y + 1, "$0.0072", 20)
    box((56, 820, 1344, 921), "#243040")
    text(78, 837, "BUDGET CHECKPOINT", 17, "#e8bd6b")
    text(78, 867, "Workers finish > report waits > raise run limit > Save and continue", 24)
    text(56, 944, "No model calls. Real AFM scheduling, usage records, budget controls and artifacts.", 18, "#95a9c6")
    image.save(Path(__file__).with_name(f"report-{scenario}.png"))


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--font", required=True, help="Path to a TrueType font")
    args = parser.parse_args()
    for scenario, workers in {
        "development": ["backend", "frontend"],
        "review": ["correctness", "security", "reliability", "performance", "contracts"],
        "refactor": ["architect-a", "architect-b"],
    }.items():
        render(scenario, workers, args.font)
