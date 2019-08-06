import { User } from '../../User.ts'
import { Request, Response } from '../../../../framework/http.ts'

/**
 * Defines the MainController.
 *
 * @export
 * @controller MainController
 */
export namespace MainController {

    /**
     * Index page for the controller.
     *
     * @export
     * @param {Request} req
     * @returns {Response}
     */
    export function index(): Response {
        return { name: 'Erik', age: 22 }
    }

    /**
     * Another page for the controller.
     *
     * @export
     * @returns {Response}
     */
    export function another(): Response {
        const name = (new User).getTable()
        return `<h1>User model table: ${name}</h1>`
    }

    /**
     * A page with two parameters.
     *
     * @export
     * @param {Request} req
     * @param {string} name
     * @param {string} page
     * @returns {Response}
     */
    export function book({ params }: Request): Response {
        return `<h1>Book: ${params.name}, page ${params.page}</h1>`
    }

}
