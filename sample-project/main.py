"""Entry point."""
from inventory import Inventory, Item
from report import render, summary


def build_demo():
    inv = Inventory()
    inv.add(Item("A-1", "Widget", 12, 3.50))
    inv.add(Item("A-2", "Gadget", 3, 12.00))
    inv.add(Item("A-3", "Gizmo", 40, 1.25))
    return inv


def run():
    inv = build_demo()
    print(render(inv))
    print(summary(inv))


if __name__ == "__main__":
    run()
