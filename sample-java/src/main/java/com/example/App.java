package com.example;

import java.util.ArrayList;
import java.util.List;

/** Trivial sample so the Java (maven) scan path has real bytecode to analyse. */
public final class App {

    private App() {
    }

    public static int add(int a, int b) {
        return a + b;
    }

    public static List<Integer> evens(int upTo) {
        List<Integer> out = new ArrayList<>();
        for (int i = 0; i <= upTo; i++) {
            if (i % 2 == 0) {
                out.add(i);
            }
        }
        return out;
    }

    public static String greet(String name) {
        if (name == null || name.isBlank()) {
            return "Hello, world!";
        }
        return "Hello, " + name + "!";
    }

    public static void main(String[] args) {
        System.out.println(greet(args.length > 0 ? args[0] : null));
        System.out.println("2 + 3 = " + add(2, 3));
        System.out.println("evens up to 10: " + evens(10));
    }
}
