"""Report rendering."""
from inventory import Inventory
from pricing import apply_tax


def render(inv):
    lines = []
    for sku, item in inv.items.items():
        lines.append(f"{sku:<10} {item.name:<20} {item.qty:>4}  {apply_tax(item.total()):>10.2f}")
    lines.append("-" * 48)
    lines.append(f"TOTAL {apply_tax(inv.value()):>42.2f}")
    return "\n".join(lines)


def summary(inv):
    return {
        "items": len(inv.items),
        "value": inv.value(),
        "low_stock": [i.name for i in inv.low_stock()],
    }
