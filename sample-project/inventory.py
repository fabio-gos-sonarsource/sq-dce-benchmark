"""Tiny sample module (smoke-test seed for sq-dce-benchmark)."""


class Item:
    def __init__(self, sku, name, qty, price):
        self.sku = sku
        self.name = name
        self.qty = qty
        self.price = price

    def total(self):
        return self.qty * self.price


class Inventory:
    def __init__(self):
        self.items = {}

    def add(self, item):
        self.items[item.sku] = item

    def remove(self, sku):
        if sku in self.items:
            del self.items[sku]

    def value(self):
        total = 0
        for sku in self.items:
            total = total + self.items[sku].total()
        return total

    def low_stock(self, threshold=5):
        result = []
        for sku, item in self.items.items():
            if item.qty < threshold:
                result.append(item)
        return result
