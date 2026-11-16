//go:build race

package translator

// Детектор гонок замедляет код в разы: временные пороги скорости в тестах под ним ослабляются.
const raceSlowdown = 6
