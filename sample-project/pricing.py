"""Pricing helpers."""
TAX = 0.2


def apply_tax(amount):
    return amount + amount * TAX


def discount(amount, pct):
    if pct < 0 or pct > 100:
        raise ValueError("pct out of range")
    return amount - (amount * pct / 100.0)


def bulk_price(unit_price, qty):
    price = unit_price * qty
    if qty > 100:
        price = discount(price, 10)
    elif qty > 50:
        price = discount(price, 5)
    return apply_tax(price)
